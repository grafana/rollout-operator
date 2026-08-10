package zpdb

import (
	"errors"
	"reflect"
	"time"

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
	podsFactory         informers.SharedInformerFactory
	podLister           corelisters.PodLister
	podsInformer        k8cache.SharedIndexInformer
	podEvictCache       *podEvictionCache
	podReadinessTracker *podReadinessTracker
	configObserver      *configObserver
	metrics             *Metrics
	logger              log.Logger
	stopCh              chan struct{}
}

func newPodObserver(kubeClient kubernetes.Interface, namespace string, readyAnnotationPatchTimeout time.Duration, configObserver *configObserver, metrics *Metrics, logger log.Logger) *podObserver {
	namespaceOpt := informers.WithNamespace(namespace)

	// initialize the ZoneAwarePodDisruptionBudget custom resource watching
	podsFactory := informers.NewSharedInformerFactoryWithOptions(kubeClient, informerSyncInterval, namespaceOpt)
	podsInformer := podsFactory.Core().V1().Pods()

	c := &podObserver{
		podsFactory:         podsFactory,
		podLister:           podsInformer.Lister(),
		podsInformer:        podsInformer.Informer(),
		podEvictCache:       newPodEvictionCache(),
		podReadinessTracker: newPodReadinessTracker(kubeClient, namespace, readyAnnotationPatchTimeout, logger),
		configObserver:      configObserver,
		metrics:             metrics,
		logger:              logger,
		stopCh:              make(chan struct{}),
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

	// Publish the metric from the moment the cache is usable, so it does not stay absent in a namespace
	// which happens to be quiet.
	c.recordInformerEvent()

	return nil
}

// recordInformerEvent stamps the time the informer's watch last told us something we did not already know.
// The eviction webhook reads pod state from the informer cache, so a watch which has silently stopped
// delivering updates means those decisions are being made on stale data with nothing else to reveal it.
func (c *podObserver) recordInformerEvent() {
	c.metrics.PodInformerLastEventTime.SetToCurrentTime()
}

// isResync reports whether an informer update is a periodic resync rather than an observed change. Resyncs
// are replayed from the cache with an unchanged resource version (the same test client-go itself uses in
// sharedIndexInformer.OnUpdate) and keep arriving even when the watch is dead, so they say nothing about
// whether the watch is still healthy.
func isResync(old, new interface{}) bool {
	oldPod, oldIsPod := old.(*corev1.Pod)
	newPod, newIsPod := new.(*corev1.Pod)
	if !oldIsPod || !newIsPod {
		return false
	}
	return oldPod.ResourceVersion == newPod.ResourceVersion
}

func (c *podObserver) invalidatePodEvictionCache(pod *corev1.Pod, action string) {
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

// accept will return a pod and true if the given object is a pod and the is within the scope of our pdb
func (c *podObserver) accept(obj interface{}) (*corev1.Pod, bool) {
	pod, isPod := obj.(*corev1.Pod)
	if !isPod {
		level.Warn(c.logger).Log("msg", "unexpected object passed through informer", "type", reflect.TypeOf(obj))
		return nil, false
	}
	pdbConfig, err := c.configObserver.pdbCache.find(pod)
	if err != nil {
		level.Warn(c.logger).Log("msg", "observer ignoring pod - unable to look up configuration for pod", "pod", pod.Name, "err", err)
		return nil, false
	}
	if pdbConfig == nil {
		level.Debug(c.logger).Log("msg", "observer ignoring pod - not within zpdb scope", "pod", pod.Name)
		return nil, false
	}

	return pod, true
}

// The informer event handlers record the event before filtering on zpdb scope: the metric tracks the health
// of the watch, which is independent of whether any given pod is covered by a zpdb.

func (c *podObserver) onPodAdded(obj interface{}) {
	c.recordInformerEvent()

	pod, ok := c.accept(obj)
	if !ok {
		return
	}

	c.podReadinessTracker.observed(pod)
	c.invalidatePodEvictionCache(pod, "added")
}

func (c *podObserver) onPodUpdated(old, new interface{}) {
	if !isResync(old, new) {
		c.recordInformerEvent()
	}

	pod, ok := c.accept(new)
	if !ok {
		return
	}

	c.podReadinessTracker.observed(pod)
	c.invalidatePodEvictionCache(pod, "updated")
}

func (c *podObserver) onPodDeleted(obj interface{}) {
	c.recordInformerEvent()

	pod, ok := c.accept(obj)
	if !ok {
		return
	}

	c.invalidatePodEvictionCache(pod, "deleted")
}

// recordEviction will mark the pod as recently evicted in the eviction cache.
// The cache entry self-expires and is also cleared when a subsequent pod state change is observed.
func (c *podObserver) recordEviction(pod *corev1.Pod) {
	c.podEvictCache.recordEviction(pod)
}

func (c *podObserver) stop() {
	close(c.stopCh)
}
