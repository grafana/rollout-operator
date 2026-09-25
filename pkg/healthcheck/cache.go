package healthcheck

import (
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// configCache holds RolloutHealthCheck configs keyed by name.
type configCache struct {
	entries map[string]*Config
	lock    sync.RWMutex
}

func newConfigCache() *configCache {
	return &configCache{
		entries: map[string]*Config{},
	}
}

func (c *configCache) addOrUpdateRaw(obj *unstructured.Unstructured) (bool, int64, error) {
	cfg, err := ParseAndValidate(obj)
	if err != nil {
		return false, -1, err
	}
	updated, generation := c.addOrUpdate(cfg)
	return updated, generation, nil
}

func (c *configCache) addOrUpdate(cfg *Config) (bool, int64) {
	c.lock.Lock()
	defer c.lock.Unlock()
	oldCfg, found := c.entries[cfg.Name]
	if found && oldCfg.Generation >= cfg.Generation {
		return false, oldCfg.Generation
	}
	c.entries[cfg.Name] = cfg
	return true, cfg.Generation
}

func (c *configCache) delete(generation int64, name string) (bool, int64) {
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

// Get returns the cached config for name, or nil if missing.
func (c *configCache) Get(name string) *Config {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return c.entries[name]
}

func (c *configCache) invalidate(name string) {
	c.lock.Lock()
	defer c.lock.Unlock()
	delete(c.entries, name)
}
