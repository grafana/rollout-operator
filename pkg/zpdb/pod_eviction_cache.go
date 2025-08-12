package zpdb

import (
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
)

const (
	// ms the eviction is cached for. This only needs to be long enough for an eviction webhook response to have triggered a pod state change.
	cacheExpiryMs = 5000
)

type PodEvictionCache struct {
	// pod name --> expiry time
	expiry sync.Map
}

// NewPodEvictionCache returns a new PodEvictionCache
// The purpose of this cache is to temporarily store a record that a pod has been evicted.
// It is expected that the pod will be deleted from this cache once the pod status has been updated after the eviction response.
// This cache can be used to mark a pod as not ready to cover the time between being marked for eviction from the pod eviction handler
// to the time the pod status is changed and reported to the rollout-operator / pdb_observer controller.
func NewPodEvictionCache() *PodEvictionCache {
	return &PodEvictionCache{
		expiry: sync.Map{},
	}
}

// RecordEviction mark a pod as having been evicted.
// The entry will remain valid for either the expiry period or until the pod entry is deleted.
func (c *PodEvictionCache) RecordEviction(pod *corev1.Pod) {
	// note that no additional concurrency checks are needed.
	// multiple Evict() calls for the same pod are unlikely, and if the expiresAt does not need to be precise.
	// the pod is expected to be deleted shortly after being stored here.
	// note also that the pod.Name is used as the key rather than pod.UID, as the UID will change if the pod is deleted or recreated
	expiresAt := time.Now().Add(time.Millisecond * cacheExpiryMs)
	c.expiry.Store(pod.Name, expiresAt)
}

// HasPendingEviction returns true if this pod is in the eviction zpdb
func (c *PodEvictionCache) HasPendingEviction(pod *corev1.Pod) bool {
	// note that we do not clean up expired entries, as the assumption is the entry will be deleted shortly after being stored here
	expiresAt, exists := c.expiry.Load(pod.Name)
	return exists && time.Now().Before(expiresAt.(time.Time))
}

// Delete removes this pod from the eviction cache
func (c *PodEvictionCache) Delete(pod *corev1.Pod) {
	c.expiry.Delete(pod.Name)
}

func (c *PodEvictionCache) Size() int {
	return c.Size()
}
