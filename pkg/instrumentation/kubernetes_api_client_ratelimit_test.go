package instrumentation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
)

// newTestThrottledCounter returns an unregistered counter for the per-API-group rate limiter. In
// production this counter is always non-nil (created via promauto), so RoundTrip increments it without a
// nil check; tests must provide one too.
func newTestThrottledCounter() *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_rate_limited_total", Help: "test"}, []string{"api_group"})
}

func TestPathToAPIGroup(t *testing.T) {
	testCases := map[string]string{
		// Every (method, path) the rollout-operator actually issues maps to one of these groups: core,
		// apps, admissionregistration.k8s.io, rollout-operator.grafana.com, plus the /api and /apis
		// discovery roots. The cases below cover that full set, including subresources and the CRD scale.

		// Core API group (/api/v1/...): pods, configmaps, secrets.
		"/api/v1/namespaces/the-namespace/pods":                  "core",
		"/api/v1/namespaces/the-namespace/pods/the-pod":          "core",
		"/api/v1/namespaces/the-namespace/configmaps/the-config": "core",
		"/api/v1/namespaces/the-namespace/secrets/the-secret":    "core",
		// apps API group, including the status subresource.
		"/apis/apps/v1/namespaces/the-namespace/statefulsets":                "apps",
		"/apis/apps/v1/namespaces/the-namespace/statefulsets/the-sts":        "apps",
		"/apis/apps/v1/namespaces/the-namespace/statefulsets/the-sts/status": "apps",
		// admissionregistration.k8s.io API group (cluster-scoped), validating and mutating.
		"/apis/admissionregistration.k8s.io/v1/validatingwebhookconfigurations": "admissionregistration.k8s.io",
		"/apis/admissionregistration.k8s.io/v1/mutatingwebhookconfigurations":   "admissionregistration.k8s.io",
		// rollout-operator.grafana.com CRD group: ZPDBs, and replicatemplates with scale/status subresources.
		"/apis/rollout-operator.grafana.com/v1/namespaces/the-namespace/zoneawarepoddisruptionbudgets":  "rollout-operator.grafana.com",
		"/apis/rollout-operator.grafana.com/v1/namespaces/the-namespace/replicatemplates/the-rt/scale":  "rollout-operator.grafana.com",
		"/apis/rollout-operator.grafana.com/v1/namespaces/the-namespace/replicatemplates/the-rt/status": "rollout-operator.grafana.com",
		// Discovery roots.
		"/api":  "core",
		"/apis": "apis",
		// Partial paths (should never happen in practice, but must still classify sensibly).
		"/apis/apps":    "apps",
		"/apis/apps/v1": "apps",
		// Non-API endpoints get bucketed by their first segment.
		"":            "/",
		"/":           "/",
		"/healthz":    "/healthz",
		"/version":    "/version",
		"/openapi/v2": "/openapi",
	}

	for path, expected := range testCases {
		t.Run(path, func(t *testing.T) {
			require.Equal(t, expected, pathToAPIGroup(path))
		})
	}
}

// recordingRoundTripper records the requests it serves and returns a canned 200 response.
type recordingRoundTripper struct {
	mu    sync.Mutex
	paths []string
}

func (rt *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.paths = append(rt.paths, req.URL.Path)
	rt.mu.Unlock()
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

func (rt *recordingRoundTripper) count() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return len(rt.paths)
}

func doRequest(ctx context.Context, rt http.RoundTripper, path string) (*http.Response, error) {
	req := httptest.NewRequest(http.MethodGet, "https://kubernetes.default.svc"+path, nil).WithContext(ctx)
	return rt.RoundTrip(req)
}

func TestPerAPIGroupRateLimiter_BucketsAreIndependentPerGroup(t *testing.T) {
	next := &recordingRoundTripper{}
	// qps=1/burst=1: each group starts with a single token and refills only once per second, so within
	// the test no bucket refills. This lets us assert that exhausting one group's bucket does not affect
	// another group's bucket.
	limiter := newPerAPIGroupRateLimiter(next, 1, 1, newTestThrottledCounter())

	// First request to the apps group consumes its only token.
	resp, err := doRequest(context.Background(), limiter, "/apis/apps/v1/namespaces/ns/statefulsets")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// A second apps request would need to wait ~1s for a token. With a short deadline the limiter returns
	// an error without ever calling the underlying transport.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = doRequest(ctx, limiter, "/apis/apps/v1/namespaces/ns/statefulsets")
	require.Error(t, err)

	// The core group has its own bucket, so its first request still succeeds immediately.
	resp, err = doRequest(context.Background(), limiter, "/api/v1/namespaces/ns/pods")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// The underlying transport was reached exactly twice: the first apps request and the core request.
	require.Equal(t, 2, next.count())
}

func TestPerAPIGroupRateLimiter_ReusesBucketPerGroup(t *testing.T) {
	limiter := newPerAPIGroupRateLimiter(&recordingRoundTripper{}, 1, 1, newTestThrottledCounter())

	first := limiter.limiterFor("apps")
	second := limiter.limiterFor("apps")
	other := limiter.limiterFor("core")

	require.Same(t, first, second, "the same API group must reuse the same limiter instance")
	require.NotSame(t, first, other, "different API groups must get different limiter instances")
}

func TestPerAPIGroupRateLimiter_ConcurrentLimiterForCreatesOneBucketPerGroup(t *testing.T) {
	// Exercises the double-checked locking in limiterFor under concurrency (run with -race): many
	// goroutines racing to create buckets for the same groups must end up with exactly one bucket per
	// distinct group, never duplicates. qps=0 yields no-op limiters; we are testing creation, not throttling.
	limiter := newPerAPIGroupRateLimiter(&recordingRoundTripper{}, 0, 0, newTestThrottledCounter())

	groups := []string{"core", "apps", "policy", "rollout-operator.grafana.com"}
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			limiter.limiterFor(groups[i%len(groups)])
		}(i)
	}
	wg.Wait()

	require.Len(t, limiter.limiters, len(groups), "exactly one bucket must be created per distinct group")
}

func TestPerAPIGroupRateLimiter_DisabledWhenNonPositive(t *testing.T) {
	for name, tc := range map[string]struct {
		qps   float32
		burst int
	}{
		"zero qps":   {qps: 0, burst: 10},
		"zero burst": {qps: 5, burst: 0},
		"negative":   {qps: -1, burst: -1},
	} {
		t.Run(name, func(t *testing.T) {
			next := &recordingRoundTripper{}
			limiter := newPerAPIGroupRateLimiter(next, tc.qps, tc.burst, newTestThrottledCounter())

			// Many rapid requests to the same group must all pass through without being throttled, even
			// with an already-cancelled context (the no-op limiter ignores it).
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			for i := 0; i < 100; i++ {
				resp, err := doRequest(ctx, limiter, "/api/v1/namespaces/ns/pods")
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, resp.StatusCode)
			}
			require.Equal(t, 100, next.count())
		})
	}
}

func TestPerAPIGroupRateLimiter_PropagatesTransportError(t *testing.T) {
	wantErr := &http.ProtocolError{ErrorString: "boom"}
	next := roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, wantErr })
	limiter := newPerAPIGroupRateLimiter(next, 5, 10, newTestThrottledCounter())

	_, err := doRequest(context.Background(), limiter, "/api/v1/namespaces/ns/pods")
	require.ErrorIs(t, err, wantErr)
}

func TestPerAPIGroupRateLimiter_ThrottleErrorIsAttributed(t *testing.T) {
	next := &recordingRoundTripper{}
	limiter := newPerAPIGroupRateLimiter(next, 1, 1, newTestThrottledCounter())

	// An already-cancelled context makes the limiter's Wait fail immediately. The returned error must be
	// wrapped so it is attributable to our client-side limiter and the API group, while preserving the
	// underlying cause for errors.Is. It must not reach the underlying transport.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := doRequest(ctx, limiter, "/apis/apps/v1/namespaces/ns/statefulsets")
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	require.Contains(t, err.Error(), "client-side rate limiter")
	require.Contains(t, err.Error(), `"apps"`)
	require.Zero(t, next.count(), "a throttled request must not reach the underlying transport")
}

func TestLimitKubernetesAPIClientPerAPIGroup_WiresConfig(t *testing.T) {
	cfg := &rest.Config{}
	LimitKubernetesAPIClientPerAPIGroup(cfg, 5, 10, nil)

	// client-go's own limiter is replaced with a no-op so it doesn't double-throttle or override us.
	require.NotNil(t, cfg.RateLimiter)
	require.True(t, cfg.RateLimiter.TryAccept(), "client-go rate limiter must be the no-op (always accepts)")

	// The transport wrapper is installed and produces our per-API-group limiter.
	require.NotNil(t, cfg.WrapTransport)
	wrapped := cfg.WrapTransport(&recordingRoundTripper{})
	require.IsType(t, &perAPIGroupRateLimiter{}, wrapped)
}

func TestLimitKubernetesAPIClientPerAPIGroup_IndependentBucketsPerTransport(t *testing.T) {
	cfg := &rest.Config{}
	LimitKubernetesAPIClientPerAPIGroup(cfg, 5, 10, nil)

	// Each time the transport wrapper is applied (i.e. for each dedicated HTTP client / component) it must
	// produce a fresh rate limiter with its own buckets, so that components built from the same config do
	// not share rate limiter state. This is what isolates one overloaded webhook from the others.
	rl1 := cfg.WrapTransport(&recordingRoundTripper{}).(*perAPIGroupRateLimiter)
	rl2 := cfg.WrapTransport(&recordingRoundTripper{}).(*perAPIGroupRateLimiter)
	require.NotSame(t, rl1, rl2)

	rl1.limiterFor("apps")
	require.Contains(t, rl1.limiters, "apps")
	require.NotContains(t, rl2.limiters, "apps", "buckets must not be shared between transports")
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
