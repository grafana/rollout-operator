package zpdb

import (
	"errors"
	"reflect"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
)

const (
	// How frequently informers should resync
	informerSyncInterval = 5 * time.Minute
)

// An ConfigObserver facilitates listening for ZoneAwarePodDisruptionBudget changes, parsing and storing these into the Cache.
// The ConfigObserver is used by the pod eviction web hook handler.
type ConfigObserver struct {
	pdbFactory  dynamicinformer.DynamicSharedInformerFactory
	pdbInformer cache.SharedIndexInformer
	PdbCache    *Cache

	dynamicClient dynamic.Interface
	logger        log.Logger

	// Used to signal when the controller should stop.
	stopCh chan struct{}
}

func NewConfigObserver(dynamic dynamic.Interface, namespace string, logger log.Logger) *ConfigObserver {
	gvr := schema.GroupVersionResource{
		Group:    ZoneAwarePodDisruptionBudgetsSpecGroup,
		Version:  ZoneAwarePodDisruptionBudgetsVersion,
		Resource: ZoneAwarePodDisruptionBudgetsNamePlural,
	}
	pdbFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		dynamic,
		informerSyncInterval,
		namespace,
		nil,
	)
	pdbInformer := pdbFactory.ForResource(gvr)

	c := &ConfigObserver{
		pdbFactory:    pdbFactory,
		pdbInformer:   pdbInformer.Informer(),
		PdbCache:      NewCache(),
		dynamicClient: dynamic,
		logger:        logger,
		stopCh:        make(chan struct{}),
	}

	return c
}

func (c *ConfigObserver) Start() error {
	_, err := c.pdbInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.onPdbAdded,
		UpdateFunc: c.onPdbUpdated,
		DeleteFunc: c.onPdbDeleted,
	})
	if err != nil {
		return err
	}

	go c.pdbFactory.Start(c.stopCh)

	// Wait until all informer caches have been synced.
	level.Info(c.logger).Log("msg", "zpdb config informer caches are syncing")
	if ok := cache.WaitForCacheSync(c.stopCh, c.pdbInformer.HasSynced); !ok {
		return errors.New("zpdb config informer caches failed to sync")
	}
	level.Info(c.logger).Log("msg", "zpdb config informer caches have synced")

	return nil
}

func (c *ConfigObserver) onPdbAdded(obj interface{}) {
	unstructuredObj, isUnstructured := obj.(*unstructured.Unstructured)
	if !isUnstructured {
		level.Warn(c.logger).Log("msg", "unexpected object passed through informer", "type", reflect.TypeOf(obj))
		return
	}

	updated, generation, err := c.PdbCache.AddOrUpdateRaw(unstructuredObj)
	if err != nil {
		level.Error(c.logger).Log("msg", "error parsing zpdb configuration", "name", unstructuredObj.GetName(), "err", err)
	} else if updated {
		level.Info(c.logger).Log("msg", "zpdb configuration updated", "name", unstructuredObj.GetName(), "generation", generation)
	} else {
		level.Info(c.logger).Log("msg", "zpdb configuration update ignored", "name", unstructuredObj.GetName(), "generation", generation, "ignored-generation", unstructuredObj.GetGeneration())
	}
}

func (c *ConfigObserver) onPdbUpdated(old, new interface{}) {
	oldCfg, oldIsUnstructured := old.(*unstructured.Unstructured)
	newCfg, newIsUnstructured := new.(*unstructured.Unstructured)

	if !oldIsUnstructured || !newIsUnstructured {
		level.Warn(c.logger).Log("msg", "unexpected object passed through informer", "old_type", reflect.TypeOf(old), "new_type", reflect.TypeOf(new))
		return
	}

	if oldCfg.GetGeneration() != newCfg.GetGeneration() {
		updated, generation, err := c.PdbCache.AddOrUpdateRaw(newCfg)
		if err != nil {
			level.Error(c.logger).Log("msg", "zpdb configuration error", "name", newCfg.GetName(), "err", err)
		} else if updated {
			level.Info(c.logger).Log("msg", "zpdb configuration updated", "name", newCfg.GetName(), "generation", generation)
		} else {
			level.Info(c.logger).Log("msg", "zpdb configuration update ignored", "name", newCfg.GetName(), "generation", generation, "ignored-generation", newCfg.GetGeneration())
		}
	}
}

func (c *ConfigObserver) onPdbDeleted(obj interface{}) {
	oldCfg, oldIsUnstructured := obj.(*unstructured.Unstructured)
	if !oldIsUnstructured {
		level.Warn(c.logger).Log("msg", "unexpected object passed through informer", "type", reflect.TypeOf(obj))
		return
	}
	success, generation := c.PdbCache.Delete(oldCfg.GetGeneration(), oldCfg.GetName())
	if success {
		level.Info(c.logger).Log("msg", "zpdb configuration deleted", "old", oldCfg.GetName(), "generation", generation)
	} else {
		level.Info(c.logger).Log("msg", "zpdb configuration delete ignored", "old", oldCfg.GetName(), "generation", generation)
	}
}

func (c *ConfigObserver) Stop() {
	close(c.stopCh)
}
