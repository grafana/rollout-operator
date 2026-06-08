// SPDX-License-Identifier: Apache-2.0

package admission

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/admission/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/flowcontrol"
)

// TestServe_HandlerDeadlinePreventsRateLimiterStall reproduces the production incident and verifies the fix.
//
// The incident: a burst of concurrent admission webhook requests (many pods being evicted at once, e.g.
// during node draining) saturated the client-side per-API-group rate limiter. Webhook request contexts are
// cancelled when the connection closes but carry no deadline, so the token bucket reserved tokens
// arbitrarily far in the future and, under concurrency, the cancellations failed to restore them. Its
// reservation timeline ran away and the operator's Kubernetes API throughput collapsed to near zero, so
// webhook calls failed with "context canceled" and evictions were denied for a sustained period.
//
// The test drives concurrent requests through Serve on such deadline-less contexts; a client-side
// token-bucket rate limiter waits on whatever context Serve hands the admit func.
func TestServe_HandlerDeadlinePreventsRateLimiterStall(t *testing.T) {
	const (
		qps      = 200
		burst    = 1
		lifetime = 100 * time.Millisecond
		dur      = 1 * time.Second
		workers  = 30
	)

	// serveThroughput drives concurrent webhook requests through Serve for dur, using deadline-less request
	// contexts that are cancelled after lifetime — exactly like an apiserver webhook connection that carries
	// no deadline and is closed when the apiserver's webhook timeout elapses. The admit func performs one
	// rate-limited "Kubernetes API call" (limiter.Wait) using the context Serve provides, simulating the
	// client-side per-API-group rate limiter. It returns how many of those calls obtained a token, and the
	// wall-clock duration over which they were granted (slightly more than dur, since in-flight requests
	// finish after the deadline).
	serveThroughput := func(handlerTimeout time.Duration) (served int64, elapsed time.Duration) {
		// A minimal v1 AdmissionReview that Serve can decode and dispatch to the admit func.
		minimalAdmissionReview := []byte(`{"apiVersion":"admission.k8s.io/v1","kind":"AdmissionReview","request":{"uid":"test-uid"}}`)

		limiter := flowcontrol.NewTokenBucketRateLimiter(qps, burst)
		admit := func(ctx context.Context, _ log.Logger, _ v1.AdmissionReview, _ *kubernetes.Clientset) *v1.AdmissionResponse {
			if err := limiter.Wait(ctx); err == nil {
				atomic.AddInt64(&served, 1)
			}
			return &v1.AdmissionResponse{Allowed: true}
		}
		handler := Serve(admit, log.NewNopLogger(), nil, handlerTimeout)

		start := time.Now()
		stop := start.Add(dur)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for time.Now().Before(stop) {
					ctx, cancel := context.WithCancel(context.Background())
					timer := time.AfterFunc(lifetime, cancel)
					req := httptest.NewRequest(http.MethodPost, "/admission/pod-eviction", bytes.NewReader(minimalAdmissionReview)).WithContext(ctx)
					req.Header.Set("Content-Type", "application/json")
					handler.ServeHTTP(httptest.NewRecorder(), req)
					timer.Stop()
					cancel()
				}
			}()
		}
		wg.Wait()
		return served, time.Since(start)
	}

	// nominal is the number of tokens the bucket grants over a window: the initial burst plus qps per
	// second. We base it on the run's actual wall-clock duration rather than dur, since the workers run
	// slightly past dur (in-flight requests finish after the deadline).
	nominal := func(elapsed time.Duration) int64 { return burst + int64(qps*elapsed.Seconds()) }

	// Without a handler deadline, Serve passes the deadline-less request context straight through. Under
	// concurrency the token bucket reserves tokens arbitrarily far in the future and the cancellations
	// don't restore them, so its reservation timeline runs away and throughput collapses far below the
	// configured rate. This is the bug.
	actualServed, elapsed := serveThroughput(0)
	expectedServed := nominal(elapsed)
	t.Logf("without handler deadline: served %d/%d requests", actualServed, expectedServed)
	require.Lessf(t, actualServed, expectedServed/3,
		"expected the rate limiter to stall without a handler deadline (runaway), but it delivered %d/%d", actualServed, expectedServed)

	// With a handler deadline (the fix), the rate limiter has a finite reservation horizon, so it rejects
	// over-budget requests immediately and keeps delivering close to the configured rate.
	actualServed, elapsed = serveThroughput(90 * time.Millisecond)
	expectedServed = nominal(elapsed)
	t.Logf("with handler deadline: served %d/%d requests", actualServed, expectedServed)
	require.GreaterOrEqualf(t, actualServed, expectedServed/2,
		"expected sustained throughput with a handler deadline, but it delivered only %d/%d", actualServed, expectedServed)
}
