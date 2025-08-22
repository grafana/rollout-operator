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
	k8cache "k8s.io/client-go/tools/cache"
)

const (
	// How frequently informers should resync
	informerSyncInterval = 5 * time.Minute
)

// An configObserver facilitates listening for ZoneAwarePodDisruptionBudget changes, parsing and storing these into the configCache.
type configObserver struct {
	pdbFactory  dynamicinformer.DynamicSharedInformerFactory
	pdbInformer k8cache.SharedIndexInformer
	pdbCache    *configCache

	dynamicClient dynamic.Interface
	logger        log.Logger

	// Used to signal when the controller should stop.
	stopCh chan struct{}
}

func newConfigObserver(dynamic dynamic.Interface, namespace string, logger log.Logger) *configObserver {
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

	c := &configObserver{
		pdbFactory:    pdbFactory,
		pdbInformer:   pdbInformer.Informer(),
		pdbCache:      newConfigCache(),
		dynamicClient: dynamic,
		logger:        logger,
		stopCh:        make(chan struct{}),
	}

	return c
}

func (c *configObserver) start() error {
	_, err := c.pdbInformer.AddEventHandler(k8cache.ResourceEventHandlerFuncs{
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
	if ok := k8cache.WaitForCacheSync(c.stopCh, c.pdbInformer.HasSynced); !ok {
		return errors.New("zpdb config informer caches failed to sync")
	}
	level.Info(c.logger).Log("msg", "zpdb config informer caches have synced")

	return nil
}

func (c *configObserver) addOrUpdate(obj *unstructured.Unstructured) {
	updated, generation, err := c.pdbCache.addOrUpdateRaw(obj)
	if err != nil {
		level.Error(c.logger).Log("msg", "zpdb configuration error", "name", obj.GetName(), "err", err)
	} else if updated {
		level.Info(c.logger).Log("msg", "zpdb configuration updated", "name", obj.GetName(), "generation", generation)
	} else {
		level.Info(c.logger).Log("msg", "zpdb configuration update ignored", "name", obj.GetName(), "generation", generation, "ignored-generation", obj.GetGeneration())
	}
}

func (c *configObserver) onPdbAdded(obj interface{}) {
	unstructuredObj, isUnstructured := obj.(*unstructured.Unstructured)
	if !isUnstructured {
		level.Warn(c.logger).Log("msg", "unexpected object passed through informer", "type", reflect.TypeOf(obj))
		return
	}

	c.addOrUpdate(unstructuredObj)
}

func (c *configObserver) onPdbUpdated(old, new interface{}) {
	_, oldIsUnstructured := old.(*unstructured.Unstructured)
	newCfg, newIsUnstructured := new.(*unstructured.Unstructured)

	if !oldIsUnstructured || !newIsUnstructured {
		level.Warn(c.logger).Log("msg", "unexpected object passed through informer", "old_type", reflect.TypeOf(old), "new_type", reflect.TypeOf(new))
		return
	}

	c.addOrUpdate(newCfg)
}

func (c *configObserver) onPdbDeleted(obj interface{}) {
	oldCfg, oldIsUnstructured := obj.(*unstructured.Unstructured)
	if !oldIsUnstructured {
		level.Warn(c.logger).Log("msg", "unexpected object passed through informer", "type", reflect.TypeOf(obj))
		return
	}
	success, generation := c.pdbCache.delete(oldCfg.GetGeneration(), oldCfg.GetName())
	if success {
		level.Info(c.logger).Log("msg", "zpdb configuration deleted", "old", oldCfg.GetName(), "generation", generation)
	} else {
		level.Info(c.logger).Log("msg", "zpdb configuration delete ignored", "name", oldCfg.GetName(), "generation", generation, "ignored-generation", oldCfg.GetGeneration())
	}
}

func (c *configObserver) stop() {
	close(c.stopCh)
}
