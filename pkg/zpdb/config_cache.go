package zpdb

import (
	"errors"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// configCache holds a map of zpdb configs by name
type configCache struct {
	// name --> config
	entries map[string]*config
	lock    sync.RWMutex
}

func newConfigCache() *configCache {
	return &configCache{
		// no initial size given as we are not expecting many entries
		entries: map[string]*config{},
		lock:    sync.RWMutex{},
	}
}

// addOrUpdateRaw attempts to parse the given unstructured object into a Config and stores it in this configCache.
// An error is returned if the given object can not be parsed into a valid config.
// The bool response indicates if the config was accepted into the configCache, with the int64 reflecting the generation of the config in the configCache.
// It will be false if this given object is an older generation of what is already in the configCache.
func (c *configCache) addOrUpdateRaw(obj *unstructured.Unstructured) (bool, int64, error) {
	if pdbConfig, err := ParseAndValidate(obj); err != nil {
		return false, -1, err
	} else {
		updated, generation := c.addOrUpdate(pdbConfig)
		return updated, generation, nil
	}
}

// addOrUpdate stores the given config into this configCache.
// Returns true if the config was saved to this configCache.
// The int64 is the generation of the config in the configCache.
// A config will be ignored if it has an older generation then an existing entry.
func (c *configCache) addOrUpdate(pdb *config) (bool, int64) {
	c.lock.Lock()
	defer c.lock.Unlock()
	oldCfg, found := c.entries[pdb.name]
	if found && oldCfg.generation >= pdb.generation {
		return false, oldCfg.generation
	}
	c.entries[pdb.name] = pdb
	return true, pdb.generation
}

// delete removes the config from this configCache.
// Returns true if the config was removed.
// The int64 is the generation of the config which was deleted or which still resides in the configCache.
// A delete will be ignored if the given generation is older than the cached entry.
func (c *configCache) delete(generation int64, name string) (bool, int64) {
	c.lock.Lock()
	defer c.lock.Unlock()
	oldCfg, found := c.entries[name]
	if !found {
		return false, -1
	}
	if oldCfg.generation > generation {
		return false, oldCfg.generation
	}
	delete(c.entries, name)
	return true, oldCfg.generation
}

// find returns a Config for a given pod, based on the config selector matching.
// Note - to match the native Kubernetes eviction controller an error is generated if multiple selectors match a given pod.
// Note - the returned Config may be nil if no matches were found.
func (c *configCache) find(pod *corev1.Pod) (*config, error) {
	c.lock.RLock()
	defer c.lock.RUnlock()
	var pdbMatch *config
	for _, pdb := range c.entries {
		if pdb.matchesPod(pod) {
			if pdbMatch != nil {
				return nil, errors.New("multiple zoned pod disruption budgets found for pod")
			}
			pdbMatch = pdb
		}
	}
	return pdbMatch, nil
}

// size returns the number of elements in the configCache
func (c *configCache) size() int {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return len(c.entries)
}
