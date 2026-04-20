package zpdb

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLifecycle(t *testing.T) {
	cache := newPodEvictionCache()
	pod := newPodCacheTest("pod-1", "")
	// pod not yet in the zpdb
	require.False(t, cache.hasPendingEviction(pod))
	// delete no existent entry
	cache.delete(pod)
	// mark as evicted
	cache.recordEviction(pod, 0)
	require.True(t, cache.hasPendingEviction(pod))
	cache.delete(pod)
	require.False(t, cache.hasPendingEviction(pod))
}

func TestLifecycle_WithForcedDelay(t *testing.T) {
	cache := newPodEvictionCache()
	pod := newPodCacheTest("pod-1", "")
	// pod not yet in the zpdb
	require.False(t, cache.hasPendingEviction(pod))
	// delete no existent entry
	cache.delete(pod)
	// mark as evicted
	cache.recordEviction(pod, 5*time.Second)
	require.True(t, cache.hasPendingEviction(pod))
	// deleting the pod will not remove it from the cache until the force time expires
	cache.delete(pod)
	require.True(t, cache.hasPendingEviction(pod))

	require.Eventually(t, func() bool {
		cache.delete(pod)
		return !cache.hasPendingEviction(pod)
	}, 10*time.Second, 50*time.Millisecond)
}
