package instrumentation

import (
	"context"
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

// PodHTTPClient is an HTTP client for calling endpoints exposed directly by pods (for example the
// prepare-downscale endpoints), as opposed to the Kubernetes API server.
//
// It is a distinct named type on purpose: callers accept a *PodHTTPClient, so a Kubernetes API client
// (which also has a Do method) cannot be passed by mistake. It deliberately does NOT use the Kubernetes
// API client's transport, so pod calls are never subject to the per-API-group rate limiter
// (LimitKubernetesAPIClientPerAPIGroup) and are tracked under their own metric.
type PodHTTPClient struct {
	client *http.Client
}

// NewPodHTTPClient builds a PodHTTPClient. base is the underlying transport (nil means
// http.DefaultTransport); when clusterValidationLabel is non-empty, requests are wrapped with cluster
// validation using reporter. timeout bounds each request (0 = no client-level timeout, rely on the request
// context). Requests are instrumented under the rollout_operator_pod_http_client_request_duration_seconds
// metric.
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
		client: &http.Client{
			Timeout: timeout,
			Transport: &podHTTPClientInstrumentation{
				next: otelhttp.NewTransport(base, otelhttp.WithClientTrace(func(ctx context.Context) *httptrace.ClientTrace {
					return otelhttptrace.NewClientTrace(ctx)
				})),
				hist: hist,
			},
		},
	}
}

func (c *PodHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return c.client.Do(req)
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
