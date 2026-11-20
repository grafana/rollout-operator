package zpdb

import (
	"errors"
	"reflect"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/informers"
	appslisters "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	k8cache "k8s.io/client-go/tools/cache"

	"github.com/grafana/rollout-operator/pkg/util"
)

// podsByNameLabel indexes Pods by namespace and "name" label, which is how the live path selects a zone's Pods.
const podsByNameLabel = "zpdb-name-label"

// An podObserver listens for pod changes, invalidating the pod eviction cache on a pod state change.
type podObserver struct {
	podsFactory    informers.SharedInformerFactory
	podLister      corelisters.PodLister
	podsInformer   k8cache.SharedIndexInformer
	stsLister      appslisters.StatefulSetLister
	stsInformer    k8cache.SharedIndexInformer
	podsIndexed    bool
	podEvictCache  *podEvictionCache
	configObserver *configObserver
	logger         log.Logger
	stopCh         chan struct{}
}

// newPodObserver builds a pod observer on top of the given shared pod informer factory. The factory is
// shared with the rollout controller: both need a full view of the pods in the namespace, and a factory
// each would mean two watches and two in-memory copies of every pod.
func newPodObserver(podsFactory informers.SharedInformerFactory, configObserver *configObserver, logger log.Logger) *podObserver {
	podsInformer := podsFactory.Core().V1().Pods()
	// Registering the StatefulSet informer before the factory starts lets the shared Start call run it.
	stsInformer := podsFactory.Apps().V1().StatefulSets()

	c := &podObserver{
		podsFactory:    podsFactory,
		podLister:      podsInformer.Lister(),
		podsInformer:   podsInformer.Informer(),
		stsLister:      stsInformer.Lister(),
		stsInformer:    stsInformer.Informer(),
		podEvictCache:  newPodEvictionCache(),
		configObserver: configObserver,
		logger:         logger,
		stopCh:         make(chan struct{}),
	}

	// Without the index, cached zone reads filter every Pod in the namespace.
	err := c.podsInformer.AddIndexers(k8cache.Indexers{podsByNameLabel: func(obj any) ([]string, error) {
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			return nil, nil
		}
		name, ok := pod.Labels["name"]
		if !ok {
			return nil, nil
		}
		return []string{pod.Namespace + "/" + name}, nil
	}})
	if err != nil {
		level.Warn(logger).Log("msg", "unable to index pods by name label, cached zone reads will scan the namespace", "err", err)
	} else {
		c.podsIndexed = true
	}

	return c
}

// cacheSynced reports whether the caches behind cached denials have synced. Startup doesn't wait for the
// StatefulSet cache, so the live path serves until it has.
func (c *podObserver) cacheSynced() bool {
	return c.podsInformer.HasSynced() && c.stsInformer.HasSynced()
}

// cachedZonePods returns the cached Pods that the live path would list for the StatefulSet's zone.
// The Pods are shared with the informer and must not be modified.
func (c *podObserver) cachedZonePods(sts *appsv1.StatefulSet) ([]*corev1.Pod, error) {
	name := sts.Spec.Template.Labels["name"]
	if !c.podsIndexed {
		selector := labels.NewSelector().Add(util.MustNewLabelsRequirement("name", selection.Equals, []string{name}))
		return c.podLister.Pods(sts.Namespace).List(selector)
	}
	objs, err := c.podsInformer.GetIndexer().ByIndex(podsByNameLabel, sts.Namespace+"/"+name)
	if err != nil {
		return nil, err
	}
	pods := make([]*corev1.Pod, 0, len(objs))
	for _, obj := range objs {
		if pod, ok := obj.(*corev1.Pod); ok {
			pods = append(pods, pod)
		}
	}
	return pods, nil
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

	// Start only starts informers the shared factory has not started yet, so whichever of the two owners
	// gets here first binds the pod informer's lifetime to its stop channel and the other call is a no-op.
	go c.podsFactory.Start(c.stopCh)

	// Wait until all informer caches have been synced.
	level.Info(c.logger).Log("msg", "zpdb pod informer caches are syncing")
	if ok := k8cache.WaitForCacheSync(c.stopCh, c.podsInformer.HasSynced); !ok {
		return errors.New("zpdb pod informer failed to sync")
	}
	level.Info(c.logger).Log("msg", "zpdb pod informer caches have synced")

	return nil
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

func (c *podObserver) onPodAdded(obj interface{}) {
	pod, ok := c.accept(obj)
	if !ok {
		return
	}

	c.invalidatePodEvictionCache(pod, "added")
}

func (c *podObserver) onPodUpdated(_, new interface{}) {
	pod, ok := c.accept(new)
	if !ok {
		return
	}

	c.invalidatePodEvictionCache(pod, "updated")
}

func (c *podObserver) onPodDeleted(obj interface{}) {
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
