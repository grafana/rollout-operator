package controller

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/grafana/rollout-operator/pkg/healthcheck"
)

type mockHealthGate struct {
	mu          sync.Mutex
	calls       int
	shouldPause bool
	lastReq     healthcheck.Request
}

func (m *mockHealthGate) Evaluate(_ context.Context, req healthcheck.Request) healthcheck.Decision {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.lastReq = req
	return healthcheck.Decision{ShouldPause: m.shouldPause, Reason: "mock"}
}

func (m *mockHealthGate) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func TestRolloutController_HealthCheckGating(t *testing.T) {
	withHealthCheck := func(sts *v1.StatefulSet) {
		if sts.Annotations == nil {
			sts.Annotations = map[string]string{}
		}
		sts.Annotations[config.RolloutHealthCheckAnnotationKey] = "ingester-cell-health"
		sts.Annotations[config.RolloutMaxUnavailableAnnotationKey] = "2"
	}

	t.Run("first zone rolls without gate", func(t *testing.T) {
		gate := &mockHealthGate{shouldPause: true}
		objects := []runtime.Object{
			mockStatefulSet("ingester-zone-a", withPrevRevision(), withHealthCheck),
			mockStatefulSet("ingester-zone-b", withPrevRevision(), withHealthCheck),
			mockStatefulSetPod("ingester-zone-a-0", testPrevRevisionHash),
			mockStatefulSetPod("ingester-zone-a-1", testPrevRevisionHash),
			mockStatefulSetPod("ingester-zone-a-2", testPrevRevisionHash),
			mockStatefulSetPod("ingester-zone-b-0", testPrevRevisionHash),
			mockStatefulSetPod("ingester-zone-b-1", testPrevRevisionHash),
			mockStatefulSetPod("ingester-zone-b-2", testPrevRevisionHash),
		}
		c := newControllerWithHealthGate(t, objects, gate)
		require.NoError(t, c.reconcile(context.Background()))
		require.Equal(t, 0, gate.callCount(), "first zone must not evaluate health gate")

		pods, err := c.kubeClient.CoreV1().Pods(testNamespace).List(context.Background(), metav1.ListOptions{})
		require.NoError(t, err)
		deleted := 0
		for _, name := range []string{"ingester-zone-a-0", "ingester-zone-a-1", "ingester-zone-a-2"} {
			found := false
			for _, p := range pods.Items {
				if p.Name == name {
					found = true
					break
				}
			}
			if !found {
				deleted++
			}
		}
		require.Equal(t, testMaxUnavailable, deleted)
	})

	t.Run("gate blocks next zone", func(t *testing.T) {
		gate := &mockHealthGate{shouldPause: true}
		objects := []runtime.Object{
			mockStatefulSet("ingester-zone-a", withHealthCheck),
			mockStatefulSet("ingester-zone-b", withPrevRevision(), withHealthCheck),
			mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
			mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
			mockStatefulSetPod("ingester-zone-a-2", testLastRevisionHash),
			mockStatefulSetPod("ingester-zone-b-0", testPrevRevisionHash),
			mockStatefulSetPod("ingester-zone-b-1", testPrevRevisionHash),
			mockStatefulSetPod("ingester-zone-b-2", testPrevRevisionHash),
		}
		c := newControllerWithHealthGate(t, objects, gate)
		require.NoError(t, c.reconcile(context.Background()))
		require.Equal(t, 1, gate.callCount())
		require.Equal(t, "ingester-zone-b", gate.lastReq.NextSTS.Name)

		for _, name := range []string{"ingester-zone-b-0", "ingester-zone-b-1", "ingester-zone-b-2"} {
			_, err := c.kubeClient.CoreV1().Pods(testNamespace).Get(context.Background(), name, metav1.GetOptions{})
			require.NoError(t, err, name)
		}
	})

	t.Run("pass allows next zone", func(t *testing.T) {
		gate := &mockHealthGate{shouldPause: false}
		objects := []runtime.Object{
			mockStatefulSet("ingester-zone-a", withHealthCheck),
			mockStatefulSet("ingester-zone-b", withPrevRevision(), withHealthCheck),
			mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
			mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
			mockStatefulSetPod("ingester-zone-a-2", testLastRevisionHash),
			mockStatefulSetPod("ingester-zone-b-0", testPrevRevisionHash),
			mockStatefulSetPod("ingester-zone-b-1", testPrevRevisionHash),
			mockStatefulSetPod("ingester-zone-b-2", testPrevRevisionHash),
		}
		c := newControllerWithHealthGate(t, objects, gate)
		require.NoError(t, c.reconcile(context.Background()))
		require.Equal(t, 1, gate.callCount())

		pods, err := c.kubeClient.CoreV1().Pods(testNamespace).List(context.Background(), metav1.ListOptions{})
		require.NoError(t, err)
		remainingB := 0
		for _, p := range pods.Items {
			if strings.HasPrefix(p.Name, "ingester-zone-b") {
				remainingB++
			}
		}
		require.Equal(t, 3-testMaxUnavailable, remainingB)
	})

	t.Run("mid-zone not gated again", func(t *testing.T) {
		gate := &mockHealthGate{shouldPause: true}
		objects := []runtime.Object{
			mockStatefulSet("ingester-zone-a", withHealthCheck),
			mockStatefulSet("ingester-zone-b", withPrevRevision(), withHealthCheck),
			mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
			mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
			mockStatefulSetPod("ingester-zone-a-2", testLastRevisionHash),
			mockStatefulSetPod("ingester-zone-b-0", testLastRevisionHash),
			mockStatefulSetPod("ingester-zone-b-1", testPrevRevisionHash),
			mockStatefulSetPod("ingester-zone-b-2", testPrevRevisionHash),
		}
		c := newControllerWithHealthGate(t, objects, gate)
		require.NoError(t, c.reconcile(context.Background()))
		require.Equal(t, 0, gate.callCount())
	})

	t.Run("no annotation skips gate", func(t *testing.T) {
		gate := &mockHealthGate{shouldPause: true}
		objects := []runtime.Object{
			mockStatefulSet("ingester-zone-a"),
			mockStatefulSet("ingester-zone-b", withPrevRevision()),
			mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
			mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
			mockStatefulSetPod("ingester-zone-a-2", testLastRevisionHash),
			mockStatefulSetPod("ingester-zone-b-0", testPrevRevisionHash),
			mockStatefulSetPod("ingester-zone-b-1", testPrevRevisionHash),
			mockStatefulSetPod("ingester-zone-b-2", testPrevRevisionHash),
		}
		c := newControllerWithHealthGate(t, objects, gate)
		require.NoError(t, c.reconcile(context.Background()))
		require.Equal(t, 0, gate.callCount())
	})

	t.Run("pause continues mid-roll zone", func(t *testing.T) {
		gate := &mockHealthGate{shouldPause: true}
		objects := []runtime.Object{
			mockStatefulSet("ingester-zone-a", withHealthCheck),
			mockStatefulSet("ingester-zone-b", withPrevRevision(), withHealthCheck),
			mockStatefulSet("ingester-zone-c", withPrevRevision(), withHealthCheck),
			mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
			mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
			mockStatefulSetPod("ingester-zone-a-2", testLastRevisionHash),
			// zone-b has not started — health gate pauses it.
			mockStatefulSetPod("ingester-zone-b-0", testPrevRevisionHash),
			mockStatefulSetPod("ingester-zone-b-1", testPrevRevisionHash),
			mockStatefulSetPod("ingester-zone-b-2", testPrevRevisionHash),
			// zone-c mid-roll (sorts after b): must still progress after b is paused.
			mockStatefulSetPod("ingester-zone-c-0", testLastRevisionHash),
			mockStatefulSetPod("ingester-zone-c-1", testPrevRevisionHash),
			mockStatefulSetPod("ingester-zone-c-2", testPrevRevisionHash),
		}
		c := newControllerWithHealthGate(t, objects, gate)
		require.NoError(t, c.reconcile(context.Background()))
		require.GreaterOrEqual(t, gate.callCount(), 1)
		pods, err := c.kubeClient.CoreV1().Pods(testNamespace).List(context.Background(), metav1.ListOptions{})
		require.NoError(t, err)
		remainingC := 0
		for _, p := range pods.Items {
			if strings.HasPrefix(p.Name, "ingester-zone-c") {
				remainingC++
			}
		}
		require.Less(t, remainingC, 3, "mid-roll zone-c should continue deleting pods despite zone-b pause")
		// zone-b must remain untouched
		for _, name := range []string{"ingester-zone-b-0", "ingester-zone-b-1", "ingester-zone-b-2"} {
			_, err := c.kubeClient.CoreV1().Pods(testNamespace).Get(context.Background(), name, metav1.GetOptions{})
			require.NoError(t, err, name)
		}
	})
}

func newControllerWithHealthGate(t *testing.T, objects []runtime.Object, gate HealthGate) *RolloutController {
	t.Helper()
	kubeClient := fake.NewClientset(objects...)
	reg := prometheus.NewPedanticRegistry()
	c := NewRolloutController(kubeClient, nil, nil, nil, testClusterDomain, testNamespace, nil, 5*time.Second, reg, log.NewNopLogger(), &mockEvictionController{})
	c.SetHealthCheck(gate, nil)
	require.NoError(t, c.Init())
	t.Cleanup(c.Stop)
	return c
}
