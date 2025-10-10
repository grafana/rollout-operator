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
	k8cache "k8s.io/client-go/tools/cache"
)

// An podObserver listens for pod changes, invalidating the pod eviction cache on a pod state change.
type podObserver struct {
	podsFactory   informers.SharedInformerFactory
	podLister     corelisters.PodLister
	podsInformer  k8cache.SharedIndexInformer
	podEvictCache *podEvictionCache
	logger        log.Logger
	stopCh        chan struct{}
}

func newPodObserver(kubeClient kubernetes.Interface, namespace string, logger log.Logger) *podObserver {
	namespaceOpt := informers.WithNamespace(namespace)

	// initialize the ZoneAwarePodDisruptionBudget custom resource watching
	podsFactory := informers.NewSharedInformerFactoryWithOptions(kubeClient, informerSyncInterval, namespaceOpt)
	podsInformer := podsFactory.Core().V1().Pods()

	c := &podObserver{
		podsFactory:   podsFactory,
		podLister:     podsInformer.Lister(),
		podsInformer:  podsInformer.Informer(),
		podEvictCache: newPodEvictionCache(),
		logger:        logger,
		stopCh:        make(chan struct{}),
	}

	return c
}

func (c *podObserver) start() error {
	_, err := c.podsInformer.AddEventHandler(k8cache.ResourceEventHandlerFuncs{
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
	if ok := k8cache.WaitForCacheSync(c.stopCh, c.podsInformer.HasSynced); !ok {
		return errors.New("zpdb pod informer failed to sync")
	}
	level.Info(c.logger).Log("msg", "zpdb pod informer caches have synced")

	return nil
}

func (c *podObserver) invalidatePodEvictionCache(obj interface{}, action string) {
	pod, isPod := obj.(*corev1.Pod)
	if !isPod {
		level.Warn(c.logger).Log("msg", "unexpected object passed through informer", "type", reflect.TypeOf(obj))
		return
	}

	// reduce logging noise as this code path will be run on any pod update
	// this is cheaper than finding the zpdb config for a pod
	// and worst case if we miss an eviction configCache removal it self-expires
	hasPendingEviction, generationAtEviction := c.podEvictCache.hasPendingEvictionWithGeneration(pod)
	if !hasPendingEviction {
		return
	}

	// after an eviction request is allowed, the informer observes one or more pod updates which can show it still running
	// if another pod eviction request comes in before the first eviction takes effect this can incorrectly allow this later eviction request to proceed
	// keep the cached eviction until we observe a non-running phase or the record is expired
	if pod.Status.Phase == corev1.PodRunning {
		level.Info(c.logger).Log(
			"msg", "ignoring pod informer update - pod is still reporting as running",
			"name", pod.GetName(),
			"generation-at-eviction", generationAtEviction,
			"generation-observed", pod.Generation,
			"reason", pod.Status.Reason,
			"phase", pod.Status.Phase,
			"creation-timestamp", pod.CreationTimestamp,
			"deletion-timestamp", pod.DeletionTimestamp,
			"observed-action", action,
		)
		return
	}

	level.Info(c.logger).Log(
		"msg", "accepting pod informer update - invaliding pod eviction configCache",
		"name", pod.GetName(),
		"generation-at-eviction", generationAtEviction,
		"generation-observed", pod.Generation,
		"reason", pod.Status.Reason,
		"phase", pod.Status.Phase,
		"creation-timestamp", pod.CreationTimestamp,
		"deletion-timestamp", pod.DeletionTimestamp,
		"observed-action", action,
	)
	c.podEvictCache.delete(pod)
}

func (c *podObserver) onPodAdded(obj interface{}) {
	c.invalidatePodEvictionCache(obj, "added")
}

func (c *podObserver) onPodUpdated(_, new interface{}) {
	c.invalidatePodEvictionCache(new, "updated")
}

func (c *podObserver) onPodDeleted(obj interface{}) {
	c.invalidatePodEvictionCache(obj, "deleted")
}

func (c *podObserver) stop() {
	close(c.stopCh)
}
