package zpdb

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLifecycle(t *testing.T) {
	cache := newPodEvictionCache(time.Second * 5)
	pod := newPodCacheTest("pod-1", "")
	// pod not yet in the zpdb
	require.False(t, cache.hasPendingEviction(pod))
	// delete no existent entry
	cache.delete(pod)
	// mark as evicted
	cache.recordEviction(pod)
	require.True(t, cache.hasPendingEviction(pod))
	cache.delete(pod)
	require.False(t, cache.hasPendingEviction(pod))
}
