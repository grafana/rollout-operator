package zpdb

import (
	"fmt"
	"reflect"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/grafana/rollout-operator/pkg/config"
)

const (
	// How frequently informers should resync
	informerSyncInterval = 5 * time.Minute
)

// An Observer facilitates listening for ZoneAwarePodDisruptionBudget changes, parsing and storing these into the Cache.
// It also listens for pod changes, invalidating a PodEvictionCache on any pod state change.
// The Cache and PodEvictionCache are used by the pod eviction web hook handler.
type Observer struct {
	namespace    string
	podsFactory  informers.SharedInformerFactory
	podLister    corelisters.PodLister
	podsInformer cache.SharedIndexInformer

	pdbFactory    dynamicinformer.DynamicSharedInformerFactory
	pdbInformer   cache.SharedIndexInformer
	pdbCache      *Cache
	podEvictCache *PodEvictionCache

	dynamicClient dynamic.Interface
	logger        log.Logger

	// Used to signal when the controller should stop.
	stopCh chan struct{}
}

func NewObserver(kubeClient kubernetes.Interface, dynamic dynamic.Interface, namespace string, logger log.Logger) *Observer {
	namespaceOpt := informers.WithNamespace(namespace)

	// initialize the ZoneAwarePodDisruptionBudget custom resource watching
	podsFactory := informers.NewSharedInformerFactoryWithOptions(kubeClient, informerSyncInterval, namespaceOpt)
	podsInformer := podsFactory.Core().V1().Pods()

	gvr := schema.GroupVersionResource{
		Group:    config.ZoneAwarePodDisruptionBudgetsSpecGroup,
		Version:  config.ZoneAwarePodDisruptionBudgetsVersion,
		Resource: config.ZoneAwarePodDisruptionBudgetsNamePlural,
	}
	pdbFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		dynamic,
		informerSyncInterval,
		namespace,
		nil,
	)
	pdbInformer := pdbFactory.ForResource(gvr)

	c := &Observer{
		namespace:     namespace,
		podsFactory:   podsFactory,
		podLister:     podsInformer.Lister(),
		podsInformer:  podsInformer.Informer(),
		pdbFactory:    pdbFactory,
		pdbInformer:   pdbInformer.Informer(),
		pdbCache:      NewCache(),
		podEvictCache: NewPodEvictionCache(),
		dynamicClient: dynamic,
		logger:        logger,
		stopCh:        make(chan struct{}),
	}

	return c
}

func (c *Observer) PdbCache() *Cache {
	return c.pdbCache
}

func (c *Observer) PodEvictionCache() *PodEvictionCache {
	return c.podEvictCache
}

func (c *Observer) Init() error {
	_, err := c.pdbInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.onPdbAdded,
		UpdateFunc: c.onPdbUpdated,
		DeleteFunc: c.onPdbDeleted,
	})
	if err != nil {
		return err
	}

	_, err = c.podsInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.onPodAdded,
		UpdateFunc: c.onPodUpdated,
		DeleteFunc: c.onPodDeleted,
	})
	if err != nil {
		return err
	}

	go c.podsFactory.Start(c.stopCh)
	go c.pdbFactory.Start(c.stopCh)

	// Wait until all informer caches have been synced.
	level.Info(c.logger).Log("msg", "zpdb informer caches are syncing")
	if ok := cache.WaitForCacheSync(c.stopCh, c.podsInformer.HasSynced, c.pdbInformer.HasSynced); !ok {
		return fmt.Errorf("zpdb informer caches failed to sync. pods informer synced=%t, zpdb informer synced=%t", c.podsInformer.HasSynced(), c.pdbInformer.HasSynced())
	}
	level.Info(c.logger).Log("msg", "zpdb informer caches have synced")

	return nil
}

func (c *Observer) invalidatePodEvictionCache(obj interface{}) {
	pod, isPod := obj.(*corev1.Pod)
	if !isPod {
		level.Warn(c.logger).Log("msg", "unexpected object passed through informer", "type", reflect.TypeOf(obj))
		return
	}
	(*c.podEvictCache).Delete(pod)
}

func (c *Observer) onPodAdded(obj interface{}) {
	c.invalidatePodEvictionCache(obj)
}

func (c *Observer) onPodUpdated(_, new interface{}) {
	c.invalidatePodEvictionCache(new)
}

func (c *Observer) onPodDeleted(obj interface{}) {
	c.invalidatePodEvictionCache(obj)
}

func (c *Observer) onPdbAdded(obj interface{}) {
	unstructuredObj, isUnstructured := obj.(*unstructured.Unstructured)
	if !isUnstructured {
		level.Warn(c.logger).Log("msg", "unexpected object passed through informer", "type", reflect.TypeOf(obj))
		return
	}

	updated, err := c.pdbCache.AddOrUpdateRaw(unstructuredObj)
	if err != nil {
		level.Error(c.logger).Log("msg", "error parsing zpdb configuration", "name", unstructuredObj.GetName(), "err", err)
	} else if updated {
		level.Info(c.logger).Log("msg", "zpdb configuration updated", "name", unstructuredObj.GetName())
	} else {
		level.Info(c.logger).Log("msg", "zpdb configuration update ignored", "name", unstructuredObj.GetName())
	}
}

func (c *Observer) onPdbUpdated(old, new interface{}) {
	oldCfg, oldIsUnstructured := old.(*unstructured.Unstructured)
	newCfg, newIsUnstructured := new.(*unstructured.Unstructured)

	if !oldIsUnstructured || !newIsUnstructured {
		level.Warn(c.logger).Log("msg", "unexpected object passed through informer", "old_type", reflect.TypeOf(old), "new_type", reflect.TypeOf(new))
		return
	}

	if oldCfg.GetGeneration() != newCfg.GetGeneration() {
		updated, err := c.pdbCache.AddOrUpdateRaw(newCfg)
		if err != nil {
			level.Error(c.logger).Log("msg", "zpdb configuration error", "name", newCfg.GetName(), "err", err)
		} else if updated {
			level.Info(c.logger).Log("msg", "zpdb configuration updated", "name", newCfg.GetName())
		} else {
			level.Info(c.logger).Log("msg", "zpdb configuration update ignored", "name", newCfg.GetName())
		}
	}
}

func (c *Observer) onPdbDeleted(obj interface{}) {
	oldCfg, oldIsUnstructured := obj.(*unstructured.Unstructured)
	if !oldIsUnstructured {
		level.Warn(c.logger).Log("msg", "unexpected object passed through informer", "type", reflect.TypeOf(obj))
		return
	}
	success := c.pdbCache.Delete(oldCfg.GetGeneration(), oldCfg.GetName())
	if success {
		level.Info(c.logger).Log("msg", "zpdb configuration deleted", "old", oldCfg.GetName())
	} else {
		level.Info(c.logger).Log("msg", "zpdb configuration delete ignored", "old", oldCfg.GetName())
	}
}

func (c *Observer) Stop() {
	close(c.stopCh)
}
