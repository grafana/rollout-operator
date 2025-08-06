package zpdb

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLifecycle(t *testing.T) {
	cache := NewPodEvictionCache()
	pod := newPodCacheTest("pod-1", "")
	// pod not yet in the zpdb
	require.False(t, cache.Evicted(pod))
	// delete no existent entry
	cache.Delete(pod)
	// mark as evicted
	cache.Evict(pod)
	require.True(t, cache.Evicted(pod))
	cache.Delete(pod)
	require.False(t, cache.Evicted(pod))
}
