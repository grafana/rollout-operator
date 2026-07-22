package instrumentation

import (
	"context"
	"io"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"time"

	"github.com/grafana/dskit/instrument"
	"github.com/grafana/dskit/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const maxPodHTTPRedirects = 10

// PodHTTPClient is an HTTP client for calling endpoints exposed directly by pods (for example the
// prepare-downscale endpoints), as opposed to the Kubernetes API server.
//
// It is a distinct named type on purpose: callers accept a *PodHTTPClient, so a Kubernetes API client
// (which also has a Do method) cannot be passed by mistake. It deliberately does NOT use the Kubernetes
// API client's transport, so pod calls are never subject to the per-API-group rate limiter
// (LimitKubernetesAPIClientPerAPIGroup) and are tracked under their own metric.
//
// Redirects are followed while preserving the original method. The default net/http client rewrites
// POST/DELETE to GET on 301/302/303, which can make prepare-downscale appear to succeed without
// invoking the prepare handler. Cross-host redirects are not followed; the 3xx response is returned
// so callers can treat it as a non-2xx failure. Request bodies are not forwarded on redirect (pod
// prepare endpoints are bodyless).
type PodHTTPClient struct {
	client  *http.Client
	timeout time.Duration
}

// NewPodHTTPClient builds a PodHTTPClient. base is the underlying transport (nil means
// http.DefaultTransport); when clusterValidationLabel is non-empty, requests are wrapped with cluster
// validation using reporter. timeout bounds each request including redirect hops (0 = no client-level
// timeout, rely on the request context). Requests are instrumented under the
// rollout_operator_pod_http_client_request_duration_seconds metric.
func NewPodHTTPClient(base http.RoundTripper, clusterValidationLabel string, clusterValidationReporter middleware.InvalidClusterValidationReporter, timeout time.Duration, reg prometheus.Registerer) *PodHTTPClient {
	if base == nil {
		base = http.DefaultTransport
	}
	if clusterValidationLabel != "" {
		base = middleware.ClusterValidationRoundTripper(clusterValidationLabel, clusterValidationReporter, base)
	}

	hist := promauto.With(reg).NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "rollout_operator_pod_http_client_request_duration_seconds",
			Help:    "Time (in seconds) spent waiting for HTTP requests the rollout-operator issues directly to pod endpoints (e.g. prepare-downscale).",
			Buckets: instrument.DefBuckets,
		},
		[]string{"path", "method", "status_code"},
	)

	return &PodHTTPClient{
		timeout: timeout,
		client: &http.Client{
			// Timeout is enforced in Do across the full redirect chain; leaving it unset here
			// avoids resetting the deadline on each hop.
			Transport: &podHTTPClientInstrumentation{
				next: otelhttp.NewTransport(base, otelhttp.WithClientTrace(func(ctx context.Context) *httptrace.ClientTrace {
					return otelhttptrace.NewClientTrace(ctx)
				})),
				hist: hist,
			},
			// Disable net/http's redirect following so Do can re-issue with the original method.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (c *PodHTTPClient) Do(req *http.Request) (*http.Response, error) {
	var cancel context.CancelFunc
	if c.timeout > 0 {
		var ctx context.Context
		// Nested deadlines take the earlier of parent (e.g. webhook) and pods.client-timeout.
		ctx, cancel = context.WithTimeout(req.Context(), c.timeout)
		req = req.WithContext(ctx)
	}

	resp, err := c.doWithRedirects(req)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return resp, err
	}
	if cancel != nil {
		// Keep the timeout context alive until the caller finishes reading the body,
		// matching http.Client.Timeout behavior.
		resp.Body = &cancelOnCloseBody{ReadCloser: resp.Body, cancel: cancel}
	}
	return resp, nil
}

func (c *PodHTTPClient) doWithRedirects(req *http.Request) (*http.Response, error) {
	for redirects := 0; ; redirects++ {
		resp, err := c.client.Do(req)
		if err != nil {
			return resp, err
		}
		if resp.StatusCode < http.StatusMultipleChoices || resp.StatusCode >= http.StatusBadRequest {
			return resp, nil
		}
		if redirects >= maxPodHTTPRedirects {
			return resp, nil
		}

		loc := resp.Header.Get("Location")
		if loc == "" {
			return resp, nil
		}
		nextURL, err := req.URL.Parse(loc)
		if err != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return nil, err
		}
		// Relative Locations inherit the request host via URL.Parse; absolute cross-host redirects stop here.
		if nextURL.Host != req.URL.Host {
			return resp, nil
		}

		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()

		// Prepare endpoints are bodyless; do not forward a request body on redirect.
		next, err := http.NewRequestWithContext(req.Context(), req.Method, nextURL.String(), nil)
		if err != nil {
			return nil, err
		}
		next.Header = req.Header.Clone()
		req = next
	}
}

type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnCloseBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// podHTTPClientInstrumentation records the duration of each request to a pod endpoint. The path label is
// the request path (low cardinality for pod endpoints, e.g. /ingester/prepare-partition-downscale), unlike
// Kubernetes API paths which require parsing into a resource description.
type podHTTPClientInstrumentation struct {
	next http.RoundTripper
	hist *prometheus.HistogramVec
}

func (p *podHTTPClientInstrumentation) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()

	resp, err := p.next.RoundTrip(req)
	statusCode := "error"
	if resp != nil {
		statusCode = strconv.Itoa(resp.StatusCode)
	}
	instrument.ObserveWithExemplar(req.Context(), p.hist.WithLabelValues(req.URL.Path, req.Method, statusCode), time.Since(start).Seconds())

	return resp, err
}
