package zpdb

import (
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/pkg/errors"
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

// A ZpdbObserver facilitates listening for ZoneAwarePodDisruptionBudget changes, parsing and storing these into the ZpdbCache.
// It also listens for pod changes, invalidating a PodEvictionCache on any pod state change.
// The ZpdbCache and PodEvictionCache are used by the pod eviction web hook handler.
type ZpdbObserver struct {
	namespace    string
	podsFactory  informers.SharedInformerFactory
	podLister    corelisters.PodLister
	podsInformer cache.SharedIndexInformer

	pdbFactory    dynamicinformer.DynamicSharedInformerFactory
	pdbInformer   cache.SharedIndexInformer
	pdbCache      *ZpdbCache
	podEvictCache *PodEvictionCache

	dynamicClient dynamic.Interface
	logger        log.Logger

	// Used to signal when the controller should stop.
	stopCh chan struct{}
}

func NewPdbObserver(kubeClient kubernetes.Interface, dynamic dynamic.Interface, namespace string, logger log.Logger) *ZpdbObserver {
	namespaceOpt := informers.WithNamespace(namespace)

	// initialize the ZoneAwarePodDisruptionBudget custom resource watching
	podsFactory := informers.NewSharedInformerFactoryWithOptions(kubeClient, informerSyncInterval, namespaceOpt)
	podsInformer := podsFactory.Core().V1().Pods()

	gvr := schema.GroupVersionResource{
		Group:    namespace + ".grafana.com",
		Version:  config.ZoneAwarePodDisruptionBudgetsVersion,
		Resource: config.ZoneAwarePodDisruptionBudgetsNamePlural,
	}
	pdbFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		dynamic,
		informerSyncInterval,
		namespace,
		nil, // tweakListOptionsFunc
	)
	pdbInformer := pdbFactory.ForResource(gvr)

	c := &ZpdbObserver{
		namespace:     namespace,
		podsFactory:   podsFactory,
		podLister:     podsInformer.Lister(),
		podsInformer:  podsInformer.Informer(),
		pdbFactory:    pdbFactory,
		pdbInformer:   pdbInformer.Informer(),
		pdbCache:      NewZpdbCache(),
		podEvictCache: NewPodEvictionCache(),
		dynamicClient: dynamic,
		logger:        logger,
		stopCh:        make(chan struct{}),
	}

	return c
}

func (c *ZpdbObserver) PdbCache() *ZpdbCache {
	return c.pdbCache
}

func (c *ZpdbObserver) PodEvictionCache() *PodEvictionCache {
	return c.podEvictCache
}

func (c *ZpdbObserver) Init() error {
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
		return errors.New("zpdb informer caches failed to sync")
	}
	level.Info(c.logger).Log("msg", "zpdb informer caches have synced")

	return nil
}

func (c *ZpdbObserver) invalidatePodEvictionCache(obj interface{}) {
	pod, isPod := obj.(*corev1.Pod)
	if isPod {
		(*c.podEvictCache).Delete(pod)
	}
}

func (c *ZpdbObserver) onPodAdded(obj interface{}) {
	c.invalidatePodEvictionCache(obj)
}

func (c *ZpdbObserver) onPodUpdated(_, new interface{}) {
	c.invalidatePodEvictionCache(new)
}

func (c *ZpdbObserver) onPodDeleted(obj interface{}) {
	c.invalidatePodEvictionCache(obj)
}

func (c *ZpdbObserver) onPdbAdded(obj interface{}) {
	unstructuredObj, isUnstructured := obj.(*unstructured.Unstructured)
	if isUnstructured {
		updated, err := c.pdbCache.AddOrUpdateRaw(unstructuredObj)
		if err != nil {
			level.Error(c.logger).Log("msg", "error parsing zpdb configuration", "name", unstructuredObj.GetName(), "err", err)
		} else if updated {
			level.Info(c.logger).Log("msg", "zpdb configuration updated", "name", unstructuredObj.GetName())
		} else {
			level.Info(c.logger).Log("msg", "zpdb configuration update ignored", "name", unstructuredObj.GetName())
		}
	}
}

func (c *ZpdbObserver) onPdbUpdated(old, new interface{}) {
	oldCfg, oldIsUnstructured := old.(*unstructured.Unstructured)
	newCfg, newIsUnstructured := new.(*unstructured.Unstructured)

	if oldIsUnstructured && newIsUnstructured && oldCfg.GetGeneration() != newCfg.GetGeneration() {
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

func (c *ZpdbObserver) onPdbDeleted(obj interface{}) {
	oldCfg, oldIsUnstructured := obj.(*unstructured.Unstructured)
	if oldIsUnstructured {
		success := c.pdbCache.Delete(oldCfg.GetGeneration(), oldCfg.GetName())
		if success {
			level.Info(c.logger).Log("msg", "zpdb configuration deleted", "old", oldCfg.GetName())
		} else {
			level.Info(c.logger).Log("msg", "zpdb configuration delete ignored", "old", oldCfg.GetName())
		}
	}
}

func (c *ZpdbObserver) Stop() {
	close(c.stopCh)
}
