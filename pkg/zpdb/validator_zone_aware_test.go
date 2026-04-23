package zpdb

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func newTestValidatorZoneAware(delay time.Duration) (*validatorZoneAware, *podEvictionCache, *podReadinessCache) {
	evictionCache := newPodEvictionCache()
	readyCache := newPodReadinessCache()
	cfg := &config{crossZoneEvictionDelay: delay}
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-sts",
			UID:  types.UID("test-uid"),
		},
	}
	v := newValidatorZoneAware(sts, 3, evictionCache, readyCache, cfg, newDummyLogger())
	return v, evictionCache, readyCache
}

func TestIsReady_PodWithPendingEviction(t *testing.T) {
	v, evictionCache, _ := newTestValidatorZoneAware(0)
	pod := readyRunningPod("pod-1", 1)

	evictionCache.recordEviction(pod)

	assert.False(t, v.isReady(pod), "pod with pending eviction should not be ready")
}

func TestIsReady_PodNotRunningAndReady(t *testing.T) {
	v, _, _ := newTestValidatorZoneAware(0)
	pod := notReadyPod("pod-1", 1)

	assert.False(t, v.isReady(pod), "pod not running and ready should not be ready")
}

func TestIsReady_NoCacheRecord(t *testing.T) {
	v, _, _ := newTestValidatorZoneAware(time.Minute)
	pod := readyRunningPod("pod-1", 1)

	// No entry in the readyCache at all
	assert.True(t, v.isReady(pod), "pod with no cache record should be considered ready")
}

func TestIsReady_CacheRecordNotEvicted(t *testing.T) {
	v, _, readyCache := newTestValidatorZoneAware(time.Minute)
	pod := readyRunningPod("pod-1", 1)

	// Observed but never evicted - evicted flag is false
	readyCache.observed(pod)

	assert.True(t, v.isReady(pod), "pod observed but never evicted should be considered ready")
}

func TestIsReady_EvictedAndReadyWithinDelay(t *testing.T) {
	delay := 5 * time.Second
	v, _, readyCache := newTestValidatorZoneAware(delay)
	pod := readyRunningPod("pod-1", 1)

	// Simulate: pod was evicted, then came back ready just now
	readyCache.recordEviction(pod)
	pod.Generation = 2
	readyCache.observed(pod) // transitions to readyRunning=true with evicted=true

	assert.False(t, v.isReady(pod), "pod evicted and ready within delay should not be ready")

	require.Eventually(t, func() bool {
		return v.isReady(pod)
	}, delay*2, time.Second, "pod becomes ready after delay expires")
}

func TestIsReady_EvictedButNotYetReadyRunning(t *testing.T) {
	v, _, readyCache := newTestValidatorZoneAware(time.Millisecond)
	pod := readyRunningPod("pod-1", 1)

	// Eviction recorded, pod comes back but readyCache still has it not ready
	// (race between IsPodRunningAndReady and cache update)
	readyCache.recordEviction(pod)
	// The readyCache entry is: readyRunning=false, evicted=true

	// The pod itself IS running+ready (passes IsPodRunningAndReady),
	// but the cache hasn't been updated yet.
	// isReady should return false because readyRecord.readyRunning is false.
	assert.False(t, v.isReady(pod), "pod evicted but cache not yet updated to ready should not be ready")
}

func TestIsReady_ZeroDelay(t *testing.T) {
	v, _, readyCache := newTestValidatorZoneAware(0)
	pod := readyRunningPod("pod-1", 1)

	readyCache.recordEviction(pod)
	pod.Generation = 2
	readyCache.observed(pod)

	assert.True(t, v.isReady(pod), "pod with zero delay should be ready immediately after becoming ready")
}

func TestIsReady_PendingEvictionTakesPrecedenceOverReadyCache(t *testing.T) {
	v, evictionCache, readyCache := newTestValidatorZoneAware(0)
	pod := readyRunningPod("pod-1", 1)

	// Pod has history in readyCache and is ready
	readyCache.observed(pod)

	// But also has a pending eviction
	evictionCache.recordEviction(pod)

	assert.False(t, v.isReady(pod), "pending eviction should take precedence over ready state")
}

func TestFullLifecycle(t *testing.T) {
	delay := 5 * time.Second

	v, evictionCache, readyCache := newTestValidatorZoneAware(delay)

	// rollout-operator starts and observes running pods
	pod := readyRunningPod("pod-1", 1)
	readyCache.observed(pod)
	assert.True(t, v.isReady(pod), "initial state - pod is considered ready")

	// the pod is evicted
	evictionCache.recordEviction(pod)
	readyCache.recordEviction(pod)
	assert.False(t, v.isReady(pod), "pod is not considered ready when flagged for eviction")

	// the pod is observed as not ready
	pod = notReadyPod("pod-1", 2)
	readyCache.observed(pod)
	assert.False(t, v.isReady(pod), "pod is not ready")

	// the pod becomes ready
	pod = readyRunningPod("pod-1", 3)
	evictionCache.delete(pod)
	readyCache.observed(pod)
	assert.False(t, v.isReady(pod), "pod is not considered ready since the delay has not elapsed")

	// the validator will consider this pod ready once the delay has passed
	require.Eventually(t, func() bool {
		return v.isReady(pod)
	}, delay*2, time.Second, "pod becomes ready after delay expires")
}
