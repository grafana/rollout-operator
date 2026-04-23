package zpdb

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func readyRunningPod(name string, generation int64) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Generation: generation,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Ready: true,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				},
			},
		},
	}
}

func notReadyPod(name string, generation int64) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Generation: generation,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
		},
	}
}

func TestObserved(t *testing.T) {
	testcases := []struct {
		name    string
		pod     *corev1.Pod
		running bool
	}{
		{
			name:    "pod ready",
			pod:     readyRunningPod("pod-1", 1),
			running: true,
		},
		{
			name:    "pod not ready",
			pod:     notReadyPod("pod-1", 1),
			running: false,
		},
	}
	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			cache := newPodReadinessCache(newDummyLogger())

			_, ok := cache.get(tc.pod)
			require.False(t, ok)

			before := time.Now()
			cache.observed(tc.pod)

			val, ok := cache.get(tc.pod)
			require.True(t, ok)
			assert.Equal(t, tc.running, val.readyRunning)
			assert.False(t, val.evicted)
			assert.Equal(t, int64(1), val.generation)
			assert.False(t, val.since.Before(before))

			// validate that the original since is preserved when no state change occurs
			since := val.since
			tc.pod.Generation = 2
			cache.observed(tc.pod)
			val, ok = cache.get(tc.pod)
			require.True(t, ok)
			assert.Equal(t, int64(1), val.generation)
			assert.Equal(t, since, val.since)
		})
	}

}

func TestObserved_StaleGenerationDiscarded(t *testing.T) {
	cache := newPodReadinessCache(newDummyLogger())

	pod := notReadyPod("pod-1", 1)
	cache.observed(pod)
	cache.observed(readyRunningPod("pod-1", 3))
	cache.observed(notReadyPod("pod-1", 2))

	val, ok := cache.get(pod)
	require.True(t, ok)
	assert.True(t, val.readyRunning)
	assert.Equal(t, int64(3), val.generation)
}

func TestDeleted_StaleGenerationDiscarded(t *testing.T) {
	cache := newPodReadinessCache(newDummyLogger())

	pod := readyRunningPod("pod-1", 3)
	cache.observed(pod)
	cache.deleted(notReadyPod("pod-1", 2))

	val, ok := cache.get(pod)
	require.True(t, ok)
	assert.True(t, val.readyRunning)
	assert.Equal(t, int64(3), val.generation)
}

func TestDeleted(t *testing.T) {
	cache := newPodReadinessCache(newDummyLogger())

	pod := readyRunningPod("pod-1", 1)
	// First observe as ready
	cache.observed(pod)
	val, _ := cache.get(pod)
	require.True(t, val.readyRunning)

	// Delete should set readyRunning to false
	cache.deleted(readyRunningPod("pod-1", 2))
	val, ok := cache.get(pod)
	require.True(t, ok)
	assert.False(t, val.readyRunning)
}

func TestRecordEviction(t *testing.T) {
	cache := newPodReadinessCache(newDummyLogger())
	pod := readyRunningPod("pod-1", 1)

	cache.observed(pod)
	val, ok := cache.get(pod)
	require.True(t, ok)
	assert.True(t, val.readyRunning)
	assert.False(t, val.evicted)

	cache.recordEviction(pod)

	val, ok = cache.get(pod)
	require.True(t, ok)
	assert.False(t, val.readyRunning)
	assert.True(t, val.evicted)
	assert.Equal(t, int64(1), val.generation)
}

func TestAddOrUpdate_SameStatePreservesSinceTime(t *testing.T) {
	cache := newPodReadinessCache(newDummyLogger())
	pod := readyRunningPod("pod-1", 1)
	cache.observed(pod)

	val, _ := cache.get(pod)
	originalSince := val.since

	// Same state, higher generation - since time should not change
	pod2 := readyRunningPod("pod-1", 2)
	cache.observed(pod2)

	val, _ = cache.get(pod)
	assert.True(t, val.readyRunning)
	assert.Equal(t, originalSince, val.since, "same state should preserve since time")
}

func TestAddOrUpdate_InheritsEvictedFlag(t *testing.T) {
	cache := newPodReadinessCache(newDummyLogger())
	pod := readyRunningPod("pod-1", 1)

	// Record eviction first
	cache.recordEviction(pod)
	val, _ := cache.get(pod)
	require.True(t, val.evicted)
	require.False(t, val.readyRunning)

	// Observe the pod as ready again - evicted flag should be inherited
	pod2 := readyRunningPod("pod-1", 2)
	cache.observed(pod2)

	val, _ = cache.get(pod)
	assert.True(t, val.readyRunning)
	assert.True(t, val.evicted, "evicted flag should be inherited across state changes")
}

func TestMultiplePods(t *testing.T) {
	cache := newPodReadinessCache(newDummyLogger())
	pod1 := readyRunningPod("pod-1", 1)
	pod2 := notReadyPod("pod-2", 1)

	cache.observed(pod1)
	cache.observed(pod2)

	val1, ok := cache.get(pod1)
	require.True(t, ok)
	assert.True(t, val1.readyRunning)

	val2, ok := cache.get(pod2)
	require.True(t, ok)
	assert.False(t, val2.readyRunning)
}

func TestDeleted_InheritsEvictedFlag(t *testing.T) {
	cache := newPodReadinessCache(newDummyLogger())
	pod := readyRunningPod("pod-1", 1)

	cache.recordEviction(pod)

	// Observe as ready (evicted flag inherited)
	pod2 := readyRunningPod("pod-1", 2)
	cache.observed(pod2)

	// Delete should also inherit evicted flag
	pod3 := readyRunningPod("pod-1", 3)
	cache.deleted(pod3)

	val, _ := cache.get(pod)
	assert.False(t, val.readyRunning)
	assert.True(t, val.evicted, "evicted flag should persist through delete")
}

func TestAddOrUpdate_EqualGenerationAllowed(t *testing.T) {
	cache := newPodReadinessCache(newDummyLogger())
	pod := readyRunningPod("pod-1", 1)
	cache.observed(pod)

	// Same generation with state change should be accepted
	notReady := notReadyPod("pod-1", 1)
	cache.observed(notReady)

	val, _ := cache.get(pod)
	assert.False(t, val.readyRunning, "equal generation update should be accepted")
}
