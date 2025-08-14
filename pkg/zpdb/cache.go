package zpdb

import (
	"errors"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type Cache struct {
	// name --> config
	entries map[string]*Config
	lock    sync.RWMutex
}

func NewCache() *Cache {
	return &Cache{
		// no initial size given as we are not expecting many entries
		entries: map[string]*Config{},
		lock:    sync.RWMutex{},
	}
}

// AddOrUpdateRaw attempts to parse the given unstructured object into a Config and stores it in this cache.
// An error is returned if the given object can not be parsed into a valid config.
// The bool response indicates if the config was accepted into the cache, with the int64 reflecting the generation of the config in the cache.
// It will be false if this given object is an older version of what is already in the cache.
func (c *Cache) AddOrUpdateRaw(obj *unstructured.Unstructured) (bool, int64, error) {
	if pdbConfig, err := ParseAndValidate(obj); err != nil {
		return false, -1, err
	} else {
		updated, generation := c.addOrUpdate(pdbConfig)
		return updated, generation, nil
	}
}

// addOrUpdate stores the given config into this cache.
// Returns true if the config was saved to this cache.
// The int64 is the generation of the config in the cache.
// A config will be ignored if it has an older generation then an existing entry
func (c *Cache) addOrUpdate(pdb *Config) (bool, int64) {
	c.lock.Lock()
	defer c.lock.Unlock()
	oldCfg, found := c.entries[pdb.Name]
	if found && oldCfg.Generation >= pdb.Generation {
		return false, oldCfg.Generation
	}
	c.entries[pdb.Name] = pdb
	return true, pdb.Generation
}

// Delete removes the config from this cache.
// Returns true if the config was removed.
// The int64 is the generation of the config which was deleted or which still resides in the cache.
// A delete will be ignored if the given generation is older than the cached entry.
func (c *Cache) Delete(generation int64, name string) (bool, int64) {
	c.lock.Lock()
	defer c.lock.Unlock()
	oldCfg, found := c.entries[name]
	if !found {
		return false, -1
	}
	if oldCfg.Generation > generation {
		return false, oldCfg.Generation
	}
	delete(c.entries, name)
	return true, oldCfg.Generation
}

// Find returns a PdbConfig for a given pod, based on the config selector matching.
// Note - to match the native Kubernetes eviction controller an error is generated if multiple selectors match a given pod.
// Note - the returned Config may be nil if no matches were found.
func (c *Cache) Find(pod *corev1.Pod) (*Config, error) {
	c.lock.RLock()
	defer c.lock.RUnlock()
	var pdbMatch *Config
	for _, pdb := range c.entries {
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
func (c *Cache) Size() int {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return len(c.entries)
}
