package instrumentation

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
)

// KubernetesAPIClientRateLimitMetrics holds the metrics shared by every dedicated Kubernetes API client's
// rate limiter. Several independently rate-limited clients (see LimitKubernetesAPIClientPerAPIGroup) share
// these, distinguished by a component label, so create this once per registerer and reuse it - constructing
// it again would panic on duplicate metric registration.
type KubernetesAPIClientRateLimitMetrics struct {
	throttled *prometheus.CounterVec
	limitQPS  *prometheus.GaugeVec
}

// NewKubernetesAPIClientRateLimitMetrics creates the metrics for LimitKubernetesAPIClientPerAPIGroup. Call
// it once and pass the result to LimitKubernetesAPIClientPerAPIGroup for each client/component.
func NewKubernetesAPIClientRateLimitMetrics(reg prometheus.Registerer) *KubernetesAPIClientRateLimitMetrics {
	return &KubernetesAPIClientRateLimitMetrics{
		// Counts requests that could not acquire a token, so client-side throttling is visible in dashboards
		// rather than only surfacing as opaque context errors.
		throttled: promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
			Name: "rollout_operator_kubernetes_api_client_rate_limited_requests_total",
			Help: "Total number of Kubernetes API client requests that failed to acquire a client-side per-API-group rate limiter token and were not sent (includes requests whose context was cancelled while waiting for a token).",
		}, []string{"api_group", "component"}),
		// Set per component only while rate limiting is actually enabled for that component (the same
		// condition limiterFor uses to decide whether to install a real token bucket at all), so it can be
		// compared against that component's observed throughput to warn before it starts rejecting requests.
		// Setting it unconditionally would make that comparison divide by zero wherever rate limiting is
		// disabled.
		limitQPS: promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
			Name: "rollout_operator_kubernetes_api_client_rate_limit_qps",
			Help: "The configured -kubernetes.client-qps value, i.e. the per-API-group token bucket rate. Only exposed for a component while client-side rate limiting is enabled for it.",
		}, []string{"component"}),
	}
}

// LimitKubernetesAPIClientPerAPIGroup installs a client-side rate limiter on the Kubernetes client
// transport that enforces an independent token bucket (qps/burst) per Kubernetes API group.
//
// We rate limit at the transport (http.RoundTripper) level instead of relying on client-go's built-in
// rate limiter because client-go's limiter cannot enforce a configurable per-API-group limit:
//   - When QPS/Burst (or a custom RateLimiter) is set on the rest.Config, kubernetes.NewForConfig()
//     builds a single rate limiter shared globally across every API group.
//   - When QPS/Burst are left unset, each API group's REST client falls back to its own default limiter
//     (5 QPS / 10 burst). That happens to be per-group, but the value can't be raised without making it
//     global (see the first bullet).
//   - The flowcontrol.RateLimiter interface exposes only request-agnostic methods (Wait/Accept/TryAccept
//     carry no request information), so even a custom limiter set on the config cannot tell one API group
//     from another.
//
// To get a configurable, per-API-group limit we therefore disable client-go's own limiter (by setting a
// no-op RateLimiter on the config) and do the throttling here, where the request URL is available and we
// can route each request to the bucket for its API group.
//
// component identifies which dedicated Kubernetes client this is (e.g. "core-controller", "pod-eviction"):
// each such client gets this called on its own rest.Config, so each has fully independent buckets, but they
// report through the shared metrics in KubernetesAPIClientRateLimitMetrics, labelled by component.
//
// A non-positive qps or burst disables rate limiting (requests are never throttled by this limiter).
//
// IMPORTANT: callers must issue requests under a context that carries a deadline, otherwise the token
// bucket's reservation timeline can run away and stall under concurrency (admission.Serve imposes one for
// webhook handlers).
func LimitKubernetesAPIClientPerAPIGroup(cfg *rest.Config, qps float32, burst int, metrics *KubernetesAPIClientRateLimitMetrics, component string) {
	// Disable client-go's built-in (global) rate limiter so this per-API-group limiter is the only throttle.
	cfg.RateLimiter = flowcontrol.NewFakeAlwaysRateLimiter()

	if qps > 0 && burst > 0 {
		metrics.limitQPS.WithLabelValues(component).Set(float64(qps))
	}

	cfg.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return newPerAPIGroupRateLimiter(rt, qps, burst, metrics.throttled, component)
	})
}

// perAPIGroupRateLimiter is an http.RoundTripper that applies a separate token bucket rate limiter per
// Kubernetes API group, identified from the request URL path. Buckets are created on first use.
type perAPIGroupRateLimiter struct {
	next      http.RoundTripper
	qps       float32
	burst     int
	throttled *prometheus.CounterVec
	component string

	limitersMx sync.RWMutex
	limiters   map[string]flowcontrol.RateLimiter
}

func newPerAPIGroupRateLimiter(next http.RoundTripper, qps float32, burst int, throttled *prometheus.CounterVec, component string) *perAPIGroupRateLimiter {
	return &perAPIGroupRateLimiter{
		next:      next,
		qps:       qps,
		burst:     burst,
		throttled: throttled,
		component: component,
		limiters:  map[string]flowcontrol.RateLimiter{},
	}
}

func (r *perAPIGroupRateLimiter) RoundTrip(req *http.Request) (*http.Response, error) {
	group := pathToAPIGroup(req.URL.Path)

	if err := r.limiterFor(group).Wait(req.Context()); err != nil {
		r.throttled.WithLabelValues(group, r.component).Inc()

		// Wrap so the failure is clearly attributable to the rollout-operator's own client-side rate
		// limiter (and to which API group), instead of surfacing as a bare "context deadline exceeded" or
		// "context canceled" that looks like a server-side timeout. %w preserves errors.Is checks.
		return nil, fmt.Errorf("client-side rate limiter for Kubernetes API group %q: %w", group, err)
	}

	return r.next.RoundTrip(req)
}

// limiterFor returns the rate limiter for the given API group, creating it on first use. The common case
// (the bucket already exists) is served under a read lock, since buckets are created once per group.
func (r *perAPIGroupRateLimiter) limiterFor(group string) flowcontrol.RateLimiter {
	r.limitersMx.RLock()
	limiter, ok := r.limiters[group]
	r.limitersMx.RUnlock()
	if ok {
		return limiter
	}

	r.limitersMx.Lock()
	defer r.limitersMx.Unlock()

	// Re-check: another goroutine may have created the limiter between releasing the read lock and
	// acquiring the write lock.
	if limiter, ok = r.limiters[group]; ok {
		return limiter
	}

	if r.qps > 0 && r.burst > 0 {
		limiter = flowcontrol.NewTokenBucketRateLimiter(r.qps, r.burst)
	} else {
		// A non-positive qps or burst means "no limit": never block.
		limiter = flowcontrol.NewFakeAlwaysRateLimiter()
	}

	r.limiters[group] = limiter
	return limiter
}

// pathToAPIGroup maps a Kubernetes API request path to a stable key identifying its API group, used to
// select the per-group rate limiter bucket.
//
// Core API requests (/api/...) map to "core"; grouped API requests (/apis/<group>/...) map to "<group>".
// Any other request (health checks, discovery, etc.) is bucketed by its first path segment so that
// non-API traffic doesn't share a bucket with a real API group.
func pathToAPIGroup(path string) string {
	segments := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 3)

	switch segments[0] {
	case "api":
		return "core"
	case "apis":
		if len(segments) >= 2 && segments[1] != "" {
			return segments[1]
		}
		return "apis"
	default:
		// Empty path or non-API endpoints (e.g. /healthz, /version): bucket by first path segment.
		return "/" + segments[0]
	}
}
