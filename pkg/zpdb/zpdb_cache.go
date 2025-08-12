package zpdb

import (
	"errors"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type ZpdbCache struct {
	// name --> config
	cache map[string]*ZpdbConfig
	lock  sync.RWMutex
}

func NewZpdbCache() *ZpdbCache {
	return &ZpdbCache{
		// no initial size given as we are not expecting many entries
		cache: map[string]*ZpdbConfig{},
		lock:  sync.RWMutex{},
	}
}

// AddOrUpdateRaw attempts to parse the given unstructured object into a ZpdbConfig and stores it in this cache.
// An error is returned if the given object can not be parsed into a valid config.
// The bool response indicates if the config was accepted into the cache.
// It will be false if this given object is an older version of what is already in the cache.
func (c *ZpdbCache) AddOrUpdateRaw(obj *unstructured.Unstructured) (bool, error) {
	if pdbConfig, err := ParseAndValidate(obj); err != nil {
		return false, err
	} else {
		return c.addOrUpdate(pdbConfig), nil
	}
}

// addOrUpdate stores the given config into this cache.
// Returns true if the config was saved to this cache.
// A config will be ignored if it has an older generation then an existing entry
func (c *ZpdbCache) addOrUpdate(pdb *ZpdbConfig) bool {
	c.lock.Lock()
	defer c.lock.Unlock()
	oldCfg, found := c.cache[pdb.Name()]
	if found && oldCfg.Generation() >= pdb.Generation() {
		return false
	}
	c.cache[pdb.Name()] = pdb
	return true
}

// Delete removes the config from this cache.
// Returns true if the config was removed.
// A delete will be ignored if the given generation is older than the cached entry.
func (c *ZpdbCache) Delete(generation int64, name string) bool {
	c.lock.Lock()
	defer c.lock.Unlock()
	oldCfg, found := c.cache[name]
	if found && oldCfg.Generation() <= generation {
		delete(c.cache, name)
		return true
	}
	return false
}

// Find returns a PdbConfig for a given pod, based on the config selector matching.
// Note - to match the native Kubernetes eviction controller an error is generated if multiple selectors match a given pod.
// Note - the returned ZpdbConfig may be nil if no matches were found.
func (c *ZpdbCache) Find(pod *corev1.Pod) (*ZpdbConfig, error) {
	c.lock.RLock()
	defer c.lock.RUnlock()
	var pdbMatch *ZpdbConfig
	for _, pdb := range c.cache {
		if pdb.MatchesPod(pod) {
			if pdbMatch != nil {
				return nil, errors.New("multiple zoned pod disruption budgets found for pod")
			}
			pdbMatch = pdb
		}
	}
	return pdbMatch, nil
}

// Size returns the number of elements in the cache
func (c *ZpdbCache) Size() int {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return len(c.cache)
}
