package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	"go.uber.org/atomic"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listersv1 "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

const (
	// How frequently informers should resync. This is also the frequency at which
	// the operator reconciles even if no changes are made to the watched resources.
	informerSyncInterval = 5 * time.Minute
)

type RolloutController struct {
	kubeClient           kubernetes.Interface
	namespace            string
	informerFactory      informers.SharedInformerFactory
	statefulSetLister    listersv1.StatefulSetLister
	statefulSetsInformer cache.SharedIndexInformer
	podLister            corelisters.PodLister
	podsInformer         cache.SharedIndexInformer
	logger               log.Logger

	// This bool is true if we should trigger a reconcile.
	shouldReconcile atomic.Bool

	// Used to signal when the controller should stop.
	stopCh chan struct{}
}

func NewRolloutController(kubeClient kubernetes.Interface, namespace string, logger log.Logger) *RolloutController {
	informerFactory := informers.NewSharedInformerFactoryWithOptions(kubeClient, informerSyncInterval, informers.WithNamespace(namespace))
	statefulSetsInformer := informerFactory.Apps().V1().StatefulSets()
	podsInformer := informerFactory.Core().V1().Pods()

	c := &RolloutController{
		kubeClient:           kubeClient,
		namespace:            namespace,
		informerFactory:      informerFactory,
		statefulSetLister:    statefulSetsInformer.Lister(),
		statefulSetsInformer: statefulSetsInformer.Informer(),
		podLister:            podsInformer.Lister(),
		podsInformer:         podsInformer.Informer(),
		logger:               logger,
		stopCh:               make(chan struct{}),
	}

	// We enqueue a reconcile request each time any of the observed StatefulSets change. The UpdateFunc
	// is also called every sync period even if no changed occurred.
	c.statefulSetsInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueReconcile()
		},
		UpdateFunc: func(old, new interface{}) {
			c.enqueueReconcile()
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueReconcile()
		},
	})

	// We enqueue a reconcile request each time any of the observed Pods are updated. Reason is that we may
	// need to proceed with the rollout whenever the state of Pods change (eg. they become Ready).
	c.podsInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(old, new interface{}) {
			c.enqueueReconcile()
		},
	})

	return c
}

// Init the controller.
func (c *RolloutController) Init() error {
	// Start informers.
	go c.informerFactory.Start(c.stopCh)

	// Wait until all informers caches have been synched.
	level.Info(c.logger).Log("msg", "informer cache is synching")
	if ok := cache.WaitForCacheSync(c.stopCh, c.statefulSetsInformer.HasSynced, c.podsInformer.HasSynced); !ok {
		return errors.New("informer cache has successfully synced")
	}
	level.Info(c.logger).Log("msg", "informer caches has synced")

	return nil
}

// Run runs the controller and blocks until Stop() is called.
func (c *RolloutController) Run() {
	ctx := context.Background()

	for {
		if c.shouldReconcile.CAS(true, false) {
			if err := c.reconcile(ctx); err != nil {
				level.Warn(c.logger).Log("msg", "reconcile failed", "err", err)

				// We should try to reconcile again.
				c.shouldReconcile.Store(true)
			}
		}

		select {
		case <-c.stopCh:
			return
		case <-time.After(5 * time.Second):
			// Throttle before checking again if we should reconcile.
		}
	}
}

// Stop the controller.
func (c *RolloutController) Stop() {
	close(c.stopCh)
}

// enqueueReconcile enqueues a request to run a reconcile as soon as possible. If multiple reconcile
// requests are enqueued while
func (c *RolloutController) enqueueReconcile() {
	c.shouldReconcile.Store(true)
}

func (c *RolloutController) reconcile(ctx context.Context) error {
	level.Info(c.logger).Log("msg", "reconcile started")

	// Find all StatefulSets with the rollout group label. These are the StatefulSets managed
	// by this operator.
	sel := labels.NewSelector()
	sel = sel.Add(mustNewLabelsRequirement(RolloutGroupLabel, selection.Exists, nil))

	sets, err := c.listStatefulSets(c.namespace, sel)
	if err != nil {
		return err
	}

	// Group statefulsets by the rollout group label. Each group will be reconciled independently.
	groups := groupStatefulSetsByLabel(sets, RolloutGroupLabel)
	var reconcileErrs error
	for _, groupSets := range groups {
		if err := c.reconcileStatefulSet(ctx, groupSets); err != nil {
			reconcileErrs = multierror.Append(reconcileErrs, err)
		}
	}

	if reconcileErrs != nil {
		return reconcileErrs
	}

	level.Info(c.logger).Log("msg", "reconcile done")
	return nil
}

func (c *RolloutController) reconcileStatefulSet(ctx context.Context, sets []*v1.StatefulSet) error {
	// Sort StatefulSets to provide a deterministic behaviour.
	sortStatefulSets(sets)

	// Ensure all StatefulSets have OnDelete update strategy. If not, then we're not able to guarantee
	// that will be updated only 1 StatefulSet at a time.
	for _, sts := range sets {
		if sts.Spec.UpdateStrategy.Type != v1.OnDeleteStatefulSetStrategyType {
			return fmt.Errorf("StatefulSet %s has %s update strategy while %s is expected, skipping reconcile", sts.Name, sts.Spec.UpdateStrategy.Type, v1.OnDeleteStatefulSetStrategyType)
		}
	}

	// Find StatefulSets with some not-Ready pods.
	notReadySets := make([]*v1.StatefulSet, 0, len(sets))
	for _, sts := range sets {
		if sts.Status.Replicas != sts.Status.ReadyReplicas {
			notReadySets = append(notReadySets, sts)
		}
	}

	// Ensure there are not 2+ StatefulSets with not-Ready pods. If there are, we shouldn't proceed
	// rolling out pods and we should wait until these pods are Ready. Reason is that if there are
	// unavailable pods in multiple StatefulSets this could lead to an outage, so we want pods to
	// get back to Ready first before proceeding.
	if len(notReadySets) > 1 {
		return fmt.Errorf("%d StatefulSets have some not-Ready pods, skipping reconcile", len(notReadySets))
	}

	// If there's a StatefulSet with not-Ready pods we also want that one to be the first one to reconcile.
	if len(notReadySets) == 1 {
		sets = moveStatefulSetToFront(sets, notReadySets[0])
	}

	for _, sts := range sets {
		ongoing, err := c.updateStatefulSetPods(ctx, sts)
		if err != nil {
			// Do not continue with other StatefulSets because this StatefulSet
			// is expected to be successfully updated before proceeding with next ones.
			return errors.Wrapf(err, "failed to update StatefulSet %s", sts.Name)
		}

		if ongoing {
			// Do not continue with other StatefulSets because this StatefulSet
			// update is still ongoing.
			return nil
		}
	}

	return nil
}

func (c *RolloutController) listStatefulSets(namespace string, sel labels.Selector) ([]*v1.StatefulSet, error) {
	sets, err := c.statefulSetLister.StatefulSets(namespace).List(sel)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list StatefulSet")
	} else if len(sets) == 0 {
		return nil, nil
	}

	// In case we modify the StatefulSet we need to make a deep copy first otherwise it
	// will conflict with the cache. To keep code easier (and safer), we always make a copy.
	deepCopy := make([]*v1.StatefulSet, 0, len(sets))
	for _, sts := range sets {
		deepCopy = append(deepCopy, sts.DeepCopy())
	}

	return deepCopy, nil
}

func (c *RolloutController) listPods(namespace string, sel labels.Selector) ([]*corev1.Pod, error) {
	pods, err := c.podLister.Pods(namespace).List(sel)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list Pod")
	} else if len(pods) == 0 {
		return nil, nil
	}

	// In case we modify the Pod we need to make a deep copy first otherwise it
	// will conflict with the cache. To keep code easier (and safer), we always make a copy.
	deepCopy := make([]*corev1.Pod, 0, len(pods))
	for _, pod := range pods {
		deepCopy = append(deepCopy, pod.DeepCopy())
	}

	return deepCopy, nil
}

func (c *RolloutController) updateStatefulSetPods(ctx context.Context, sts *v1.StatefulSet) (bool, error) {
	level.Debug(c.logger).Log("msg", "reconciling StatefulSet", "statefulset", sts.Name)

	podsToUpdate, err := c.podsToUpdate(sts)
	if err != nil {
		return false, errors.Wrap(err, "failed to get pods to update")
	}

	if len(podsToUpdate) > 0 {
		maxUnavailable := getMaxUnavailableForStatefulSet(sts, c.logger)
		numNotReady := int(sts.Status.Replicas - sts.Status.ReadyReplicas)

		// Compute the number of pods we should update, honoring the configured maxUnavailable.
		numPods := max(0, min(
			maxUnavailable-numNotReady, // No more than the configured maxUnavailable (including any not-Ready pod).
			len(podsToUpdate),          // No more than the total number of pods that needs to be updated.
		))

		if numPods == 0 {
			level.Info(c.logger).Log(
				"msg", "StatefulSet has some pods to be updated but maxUnavailable pods has been reached",
				"statefulset", sts.Name,
				"pods_to_update", len(podsToUpdate),
				"replicas", sts.Status.Replicas,
				"ready_replicas", sts.Status.ReadyReplicas,
				"max_unavailable", maxUnavailable)

			return true, nil
		}

		for _, pod := range podsToUpdate[:numPods] {
			// Skip if the pod is terminating. Since "Terminating" is not a pod Phase, we can infer it checking
			// if the pod is in Running phase but the deletionTimestamp has been set (kubectl does something
			// similar too).
			if pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp != nil {
				level.Debug(c.logger).Log("msg", fmt.Sprintf("waiting for pod %s to be terminated", pod.Name))
				continue
			}

			level.Info(c.logger).Log("msg", fmt.Sprintf("terminating pod %s", pod.Name))
			if err := c.kubeClient.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil {
				return false, errors.Wrapf(err, "failed to restart pod %s", pod.Name)
			}
		}

		return true, nil
	}

	// Ensure all pods in this StatefulSet are Ready, otherwise we consider a rollout is in progress
	// (in any case, it's not safe to proceed with other StatefulSets).
	if sts.Status.Replicas != sts.Status.ReadyReplicas {
		level.Info(c.logger).Log(
			"msg", "StatefulSet pods are all updated but StatefulSet has some not-Ready replicas",
			"statefulset", sts.Name,
			"replicas", sts.Status.Replicas,
			"ready_replicas", sts.Status.ReadyReplicas)

		return true, nil
	}

	// At this point there are no pods to update, so we can update the currentRevision in the StatefulSet.
	// When the StatefulSet update strategy is RollingUpdate this is done automatically by the controller,
	// when when it's OnDelete (our case) then it's our responsibility to update it once done.
	if sts.Status.CurrentRevision != sts.Status.UpdateRevision {
		sts.Status.CurrentRevision = sts.Status.UpdateRevision

		level.Debug(c.logger).Log("msg", "updating StatefulSet current revision", "old_current_revision", sts.Status.CurrentRevision, "new_current_revision", sts.Status.UpdateRevision)
		if sts, err = c.kubeClient.AppsV1().StatefulSets(sts.Namespace).UpdateStatus(ctx, sts, metav1.UpdateOptions{}); err != nil {
			return false, errors.Wrapf(err, "failed to update StatefulSet %s", sts.Name)
		}
		level.Info(c.logger).Log("msg", "updated StatefulSet current revision", "old_current_revision", sts.Status.CurrentRevision, "new_current_revision", sts.Status.UpdateRevision)
	}

	return false, nil
}

func (c *RolloutController) podsToUpdate(sts *v1.StatefulSet) ([]*corev1.Pod, error) {
	var (
		currRev   = sts.Status.CurrentRevision
		updateRev = sts.Status.UpdateRevision
	)

	// Do NOT introduce a short circuit if "currRev == updateRev". Reason is that if a change
	// is rolled back in the StatefulSet to the previous version, the updateRev == currRev but
	// its pods may still run the previous updateRev. We need to check pods to be 100% sure.
	if currRev == "" {
		return nil, errors.New("currentRevision is empty")
	} else if updateRev == "" {
		return nil, errors.New("updateRevision is empty")
	}

	// Get any pods whose revision doesn't match the StatefulSet's updateRevision
	// and so it means they still need to be updated.
	podsSelector := labels.NewSelector().Add(
		mustNewLabelsRequirement(v1.ControllerRevisionHashLabelKey, selection.NotEquals, []string{updateRev}),
		mustNewLabelsRequirement("name", selection.Equals, []string{sts.Spec.Template.Labels["name"]}),
	)

	pods, err := c.listPods(sts.Namespace, podsSelector)
	if err != nil {
		return nil, err
	}

	// Sort pods in order to provide a deterministic behaviour.
	sortPods(pods)

	return pods, nil
}
