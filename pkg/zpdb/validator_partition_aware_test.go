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
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/grafana/rollout-operator/pkg/util"
)

func newTestValidatorPartitionAware(delay time.Duration) (*validatorPartitionAware, *podEvictionCache, *podReadinessTracker) {
	evictionCache := newPodEvictionCache()
	readyTracker := newPodReadinessTracker(k8sfake.NewClientset(), testNamespace, log.NewNopLogger())
	cfg := &config{crossZoneEvictionDelay: delay}
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-sts",
			UID:  types.UID("test-uid"),
		},
	}
	logger, _ := spanlogger.New(context.Background(), log.NewNopLogger(), "test", util.NoTenantResolver{})
	v := newValidatorPartitionAware(sts, "0", 3, cfg, evictionCache, readyTracker, logger)
	return v, evictionCache, readyTracker
}

// withReadyAnnotation sets podReadyAnnotationKey on a copy of pod's annotations.
// The validator reads this directly via readyTracker.get, so tests for isReady drive
// behavior by setting it on the in-memory pod.
func withReadyAnnotation(pod *corev1.Pod, t time.Time) *corev1.Pod {
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[podReadyAnnotationKey] = t.UTC().Format(time.RFC3339)
	return pod
}

func TestIsReady_PodWithPendingEviction(t *testing.T) {
	v, evictionCache, _ := newTestValidatorPartitionAware(0)
	pod := readyRunningPod("pod-1")

	evictionCache.recordEviction(pod)

	assert.False(t, v.isReady(pod), "pod with pending eviction should not be ready")
}

func TestIsReady_PodNotRunningAndReady(t *testing.T) {
	v, _, _ := newTestValidatorPartitionAware(0)
	pod := notReadyPod("pod-1")

	assert.False(t, v.isReady(pod), "pod that fails IsPodRunningAndReady should not be ready")
}

func TestIsReady_ZeroDelayAlwaysReady(t *testing.T) {
	v, _, _ := newTestValidatorPartitionAware(0)
	pod := readyRunningPod("pod-1")

	// With no delay configured, the validator short-circuits and treats any ready+running
	// pod as ready, regardless of the annotation.
	assert.True(t, v.isReady(pod), "ready pod with zero delay should be ready immediately")
}

func TestIsReady_PendingEvictionBeatsAnnotation(t *testing.T) {
	v, evictionCache, _ := newTestValidatorPartitionAware(time.Minute)
	pod := readyRunningPod("pod-1")
	withReadyAnnotation(pod, time.Now().Add(-time.Hour)) // annotation well outside the delay window

	evictionCache.recordEviction(pod)

	assert.False(t, v.isReady(pod), "pending eviction should override an otherwise-ready annotation")
}

func TestIsReady_NoAnnotationDenied(t *testing.T) {
	v, _, _ := newTestValidatorPartitionAware(time.Minute)
	pod := readyRunningPod("pod-1")

	// readyTracker.get returns time.Now() when no annotation is present, so the delay window
	// starts at "now" and isReady must deny until it expires. This is the safe default for
	// pods seen for the first time after a rollout-operator restart.
	assert.False(t, v.isReady(pod), "ready pod with no annotation should be denied while a delay is configured")
}

func TestIsReady_AnnotationOutsideDelayWindow(t *testing.T) {
	v, _, _ := newTestValidatorPartitionAware(time.Minute)
	pod := readyRunningPod("pod-1")
	withReadyAnnotation(pod, time.Now().Add(-time.Hour))

	assert.True(t, v.isReady(pod), "ready pod with annotation older than the delay should be ready")
}

func TestIsReady_AnnotationInsideDelayWindow(t *testing.T) {
	v, _, _ := newTestValidatorPartitionAware(time.Minute)
	pod := readyRunningPod("pod-1")
	withReadyAnnotation(pod, time.Now())

	assert.False(t, v.isReady(pod), "ready pod with annotation inside the delay window should not be ready")
}

func TestIsReady_MalformedAnnotationDenied(t *testing.T) {
	v, _, _ := newTestValidatorPartitionAware(time.Minute)
	pod := readyRunningPod("pod-1")
	pod.Annotations = map[string]string{podReadyAnnotationKey: "not-a-timestamp"}

	// readyTracker.get falls back to time.Now() when the annotation cannot be parsed, so the
	// validator should treat the pod the same as one with no annotation - denied while a
	// delay is configured.
	assert.False(t, v.isReady(pod), "malformed annotation should fall back to now() and be denied")
}

func TestIsReady_BecomesReadyAfterDelay(t *testing.T) {
	delay := time.Second
	v, _, _ := newTestValidatorPartitionAware(delay)
	pod := readyRunningPod("pod-1")
	withReadyAnnotation(pod, time.Now())

	assert.False(t, v.isReady(pod), "ready pod just-set within the delay window should not yet be ready")

	require.Eventually(t, func() bool {
		return v.isReady(pod)
	}, delay*4, 100*time.Millisecond, "pod becomes ready once wall-clock advances past annotation+delay")
}
