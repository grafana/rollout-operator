package zpdb

import (
	"sync"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	corev1 "k8s.io/api/core/v1"

	"github.com/grafana/rollout-operator/pkg/util"
)

type podReadinessCacheValue struct {
	// the time that this pod changed state
	since time.Time
	// the last observed state of the pod
	readyRunning bool
	// true this pod has been evicted since we started observing it
	evicted bool
	// the creationTimestamp of the pod - so we can detect stale pod updates
	creationTimestamp int64
}

type podReadinessCache struct {
	// pod name --> [ since time, creation timestamp ]
	entries map[string]podReadinessCacheValue
	lock    sync.RWMutex
	logger  log.Logger
}

// newPodReadinessCache returns a new podReadinessCache
// The purpose of this cache is to track the time when a pod last became ready and running.
func newPodReadinessCache(logger log.Logger) *podReadinessCache {
	return &podReadinessCache{
		// no pre-allocation as this will not grow significantly
		entries: map[string]podReadinessCacheValue{},
		lock:    sync.RWMutex{},
		logger:  logger,
	}
}

// recordEviction will add/update the cached record for this pod, setting
// ready to false and evicted to true.
// Although the pod may still be running we know it will soon be not ready.
func (c *podReadinessCache) recordEviction(pod *corev1.Pod) {
	c.lock.Lock()
	defer c.lock.Unlock()

	level.Debug(c.logger).Log("msg", "recordEviction", "pod", pod.Name, "creationTimestamp", pod.CreationTimestamp.Unix())

	c.entries[pod.Name] = podReadinessCacheValue{
		since:        time.Now(),
		readyRunning: false,
		evicted:      true,
		// Note that we do not check for stale creation timestamps since this will be explicitly called from the
		// eviction controller. It is not being called from async informers
		creationTimestamp: pod.CreationTimestamp.Unix(),
	}
}

// deleted will add/update the cached record for this pod, setting
// ready to false.
func (c *podReadinessCache) deleted(pod *corev1.Pod) {
	c.addOrUpdate(pod, false)
}

// observed will add/update the cached record for this pod, setting
// ready to util.IsPodRunningAndReady(pod).
func (c *podReadinessCache) observed(pod *corev1.Pod) {
	c.addOrUpdate(pod, util.IsPodRunningAndReady(pod))
}

// addOrUpdate will add/update the cached record for this pod, setting
// ready to the given value.
// No change is made if the pod creation timestamp is stale or the cached
// value already indicates that there is no change in ready state.
// Any existing evicted value is inherited.
func (c *podReadinessCache) addOrUpdate(pod *corev1.Pod, readyRunning bool) {
	c.lock.Lock()
	defer c.lock.Unlock()

	cachedValue, existingInCache := c.entries[pod.Name]

	level.Debug(c.logger).Log("msg", "addOrUpdate", "pod", pod.Name, "readyRunning", readyRunning, "creationTimestamp", pod.CreationTimestamp.Unix())

	// discard stale update
	if existingInCache && pod.CreationTimestamp.Unix() < cachedValue.creationTimestamp {
		return
	}

	// no change - we want to keep the previous since time
	if existingInCache && cachedValue.readyRunning == readyRunning {
		return
	}

	// inherit any existing evicted value
	evicted := false
	if existingInCache {
		evicted = cachedValue.evicted
	}

	c.entries[pod.Name] = podReadinessCacheValue{
		since:             time.Now(),
		readyRunning:      readyRunning,
		evicted:           evicted,
		creationTimestamp: pod.CreationTimestamp.Unix(),
	}
}

// get returns the current podReadinessCacheValue for the given pod.
func (c *podReadinessCache) get(pod *corev1.Pod) (podReadinessCacheValue, bool) {
	c.lock.RLock()
	defer c.lock.RUnlock()

	value, ok := c.entries[pod.Name]
	if !ok {
		return podReadinessCacheValue{}, false
	}
	return value, true
}
