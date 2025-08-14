package zpdb

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLifecycle(t *testing.T) {
	cache := NewPodEvictionCache()
	pod := newPodCacheTest("pod-1", "")
	// pod not yet in the zpdb
	require.False(t, cache.HasPendingEviction(pod))
	// delete no existent entry
	cache.Delete(pod)
	// mark as evicted
	cache.RecordEviction(pod)
	require.True(t, cache.HasPendingEviction(pod))
	cache.Delete(pod)
	require.False(t, cache.HasPendingEviction(pod))
}
