package zpdb

import (
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
)

const (
	// duration the eviction is cached for. This only needs to be long enough for an eviction webhook response to have triggered a pod state change.
	// note that kubernetes's own implementation use a 2 minute DeletionTimeout for their DisruptedPods map. Their commentary suggests 1-2 sec
	// should be sufficient. See https://github.com/kubernetes/kubernetes/blob/master/pkg/controller/disruption/disruption.go
	cacheExpiry = 2 * time.Minute
)

type podEvictionCacheValue struct {
	// the expiry time for this record
	expires time.Time
	// the generation of the pod when added
	generation int64
	// the last sampled phase
	phase corev1.PodPhase
}

type podEvictionCache struct {
	// pod name --> [ expiry time, generation ]
	entries map[string]podEvictionCacheValue
	lock    sync.RWMutex
	ttl     time.Duration
}

// newPodEvictionCache returns a new podEvictionCache
// The purpose of this cache is to temporarily store a record that a pod has been evicted.
// It is expected that the pod will be deleted from this cache once the pod status has been updated after the eviction response.
// This cache can be used to imply that a pod is not ready to cover the time between being marked for eviction until
// the time the pod status is changed and reported to the rollout-operator / pod observer.
func newPodEvictionCache(ttl time.Duration) *podEvictionCache {
	return &podEvictionCache{
		// no pre-allocation as this will not grow significantly
		entries: map[string]podEvictionCacheValue{},
		lock:    sync.RWMutex{},
		ttl:     ttl,
	}
}

// recordEviction mark a pod as having been evicted.
// The entry will remain valid for either the expiry period or until the pod entry is deleted.
func (c *podEvictionCache) recordEviction(pod *corev1.Pod) {
	// note also that the pod.Name is used as the key rather than pod.UID, as the UID will change if the pod is deleted or recreated
	expiresAt := time.Now().Add(c.ttl)
	c.lock.Lock()
	defer c.lock.Unlock()
	c.entries[pod.Name] = podEvictionCacheValue{
		expires:    expiresAt,
		generation: pod.Generation,
		phase:      pod.Status.Phase,
	}
}

// hasPendingEviction returns true if this pod is in the cache and not expired
func (c *podEvictionCache) hasPendingEviction(pod *corev1.Pod) bool {
	// note that we do not clean up expired entries, as the assumption is the entry will be deleted shortly after being stored here
	c.lock.RLock()
	defer c.lock.RUnlock()
	rec, exists := c.entries[pod.Name]
	return exists && time.Now().Before(rec.expires)
}

// hasPendingEvictionWithGeneration returns true if this pod is in the cache and not expired. It also returns the generation of the pod which was cached.
func (c *podEvictionCache) hasPendingEvictionWithGeneration(pod *corev1.Pod) (bool, int64, corev1.PodPhase) {
	c.lock.Lock()
	defer c.lock.Unlock()
	rec, exists := c.entries[pod.Name]
	if !exists || time.Now().Equal(rec.expires) || time.Now().After(rec.expires) {
		return false, 0, ""
	}

	previousPhase := rec.phase
	rec.phase = pod.Status.Phase
	c.entries[pod.Name] = rec
	return exists, rec.generation, previousPhase
}

// delete removes this pod from the cache
func (c *podEvictionCache) delete(pod *corev1.Pod) {
	c.lock.Lock()
	defer c.lock.Unlock()
	delete(c.entries, pod.Name)
}
