package zpdb

import (
	"context"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/grafana/dskit/spanlogger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/grafana/rollout-operator/pkg/util"
)

func newTestValidatorPartitionAware(delay time.Duration) (*validatorPartitionAware, *podEvictionCache) {
	evictionCache := newPodEvictionCache()
	cfg := &config{crossZoneEvictionDelay: delay}
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-sts",
			UID:  types.UID("test-uid"),
		},
	}
	logger, _ := spanlogger.New(context.Background(), log.NewNopLogger(), "test", util.NoTenantResolver{})
	v := newValidatorPartitionAware(sts, "0", 3, cfg, evictionCache, logger)
	return v, evictionCache
}

func readyRunningPod(name string, transitionedAt time.Time) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{
				Type:               corev1.PodReady,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(transitionedAt),
			}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Ready: true,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
		},
	}
}

func notReadyPod(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
}

func setReadyTransitionTime(pod *corev1.Pod, transitionedAt time.Time) {
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == corev1.PodReady {
			pod.Status.Conditions[i].LastTransitionTime = metav1.NewTime(transitionedAt)
			return
		}
	}
	pod.Status.Conditions = append(pod.Status.Conditions, corev1.PodCondition{
		Type:               corev1.PodReady,
		Status:             corev1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(transitionedAt),
	})
}

func TestIsReady_PodWithPendingEviction(t *testing.T) {
	v, evictionCache := newTestValidatorPartitionAware(0)
	pod := readyRunningPod("pod-1", time.Now())

	evictionCache.recordEviction(pod)

	assert.False(t, v.isReady(pod), "pod with pending eviction should not be ready")
}

func TestIsReady_PodNotRunningAndReady(t *testing.T) {
	v, _ := newTestValidatorPartitionAware(0)
	pod := notReadyPod("pod-1")

	assert.False(t, v.isReady(pod), "pod that fails IsPodRunningAndReady should not be ready")
}

func TestIsReady_ZeroDelayAlwaysReady(t *testing.T) {
	v, _ := newTestValidatorPartitionAware(0)
	pod := readyRunningPod("pod-1", time.Time{})

	assert.True(t, v.isReady(pod), "ready pod with zero delay should be ready immediately")
}

func TestIsReady_PendingEvictionBeatsTransitionTime(t *testing.T) {
	v, evictionCache := newTestValidatorPartitionAware(time.Minute)
	pod := readyRunningPod("pod-1", time.Now().Add(-time.Hour))

	evictionCache.recordEviction(pod)

	assert.False(t, v.isReady(pod), "pending eviction should override an otherwise-ready transition time")
}

func TestIsReady_NoReadyConditionDenied(t *testing.T) {
	v, _ := newTestValidatorPartitionAware(time.Minute)
	pod := readyRunningPod("pod-1", time.Now().Add(-time.Hour))
	pod.Status.Conditions = nil

	assert.False(t, v.isReady(pod), "ready pod without a Ready condition should be denied while a delay is configured")
}

func TestIsReady_TransitionOutsideDelayWindow(t *testing.T) {
	v, _ := newTestValidatorPartitionAware(time.Minute)
	pod := readyRunningPod("pod-1", time.Now().Add(-time.Hour))

	assert.True(t, v.isReady(pod), "ready pod with an old enough transition should be ready")
}

func TestIsReady_TransitionInsideDelayWindow(t *testing.T) {
	v, _ := newTestValidatorPartitionAware(time.Minute)
	pod := readyRunningPod("pod-1", time.Now())

	assert.False(t, v.isReady(pod), "ready pod with a recent transition should not be ready")
}

func TestIsReady_ZeroTransitionTimeDenied(t *testing.T) {
	v, _ := newTestValidatorPartitionAware(time.Minute)
	pod := readyRunningPod("pod-1", time.Time{})

	assert.False(t, v.isReady(pod), "zero transition time should be denied")
}

func TestIsReady_FutureTransitionTimeDenied(t *testing.T) {
	v, _ := newTestValidatorPartitionAware(time.Minute)
	pod := readyRunningPod("pod-1", time.Now().Add(time.Hour))

	assert.False(t, v.isReady(pod), "future transition time should be denied")
}

func TestIsReady_BecomesReadyAfterDelay(t *testing.T) {
	delay := time.Second
	v, _ := newTestValidatorPartitionAware(delay)
	pod := readyRunningPod("pod-1", time.Now())

	assert.False(t, v.isReady(pod), "ready pod just-set within the delay window should not yet be ready")

	require.Eventually(t, func() bool {
		return v.isReady(pod)
	}, delay*4, 100*time.Millisecond, "pod becomes ready once wall-clock advances past Ready.LastTransitionTime+delay")
}

func TestIsReady_ReadyTransitionResetsDelay(t *testing.T) {
	delay := time.Minute
	v, _ := newTestValidatorPartitionAware(delay)
	pod := readyRunningPod("pod-1", time.Now().Add(-time.Hour))
	require.True(t, v.isReady(pod))

	setReadyTransitionTime(pod, time.Now())
	assert.False(t, v.isReady(pod), "a new Ready transition should restart the delay")
}
