package zpdb

import (
	"errors"
	"reflect"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

// An PodObserver listens for pod changes, invalidating a PodEvictionCache on any pod state change.
// The PodObserver is used by the pod eviction web hook handler.
type PodObserver struct {
	namespace     string
	podsFactory   informers.SharedInformerFactory
	podLister     corelisters.PodLister
	podsInformer  cache.SharedIndexInformer
	PodEvictCache *PodEvictionCache
	logger        log.Logger
	stopCh        chan struct{}
}

func NewPodObserver(kubeClient kubernetes.Interface, namespace string, logger log.Logger) *PodObserver {
	namespaceOpt := informers.WithNamespace(namespace)

	// initialize the ZoneAwarePodDisruptionBudget custom resource watching
	podsFactory := informers.NewSharedInformerFactoryWithOptions(kubeClient, informerSyncInterval, namespaceOpt)
	podsInformer := podsFactory.Core().V1().Pods()

	c := &PodObserver{
		namespace:     namespace,
		podsFactory:   podsFactory,
		podLister:     podsInformer.Lister(),
		podsInformer:  podsInformer.Informer(),
		PodEvictCache: NewPodEvictionCache(),
		logger:        logger,
		stopCh:        make(chan struct{}),
	}

	return c
}

func (c *PodObserver) Start() error {
	_, err := c.podsInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.onPodAdded,
		UpdateFunc: c.onPodUpdated,
		DeleteFunc: c.onPodDeleted,
	})
	if err != nil {
		return err
	}

	go c.podsFactory.Start(c.stopCh)

	// Wait until all informer caches have been synced.
	level.Info(c.logger).Log("msg", "zpdb pod informer caches are syncing")
	if ok := cache.WaitForCacheSync(c.stopCh, c.podsInformer.HasSynced); !ok {
		return errors.New("zpdb pod informer failed to sync")
	}
	level.Info(c.logger).Log("msg", "zpdb pod informer caches have synced")

	return nil
}

func (c *PodObserver) invalidatePodEvictionCache(obj interface{}) {
	pod, isPod := obj.(*corev1.Pod)
	if !isPod {
		level.Warn(c.logger).Log("msg", "unexpected object passed through informer", "type", reflect.TypeOf(obj))
		return
	}
	(*c.PodEvictCache).Delete(pod)
}

func (c *PodObserver) onPodAdded(obj interface{}) {
	c.invalidatePodEvictionCache(obj)
}

func (c *PodObserver) onPodUpdated(_, new interface{}) {
	c.invalidatePodEvictionCache(new)
}

func (c *PodObserver) onPodDeleted(obj interface{}) {
	c.invalidatePodEvictionCache(obj)
}

func (c *PodObserver) Stop() {
	close(c.stopCh)
}
