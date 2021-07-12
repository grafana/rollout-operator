package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
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

type Controller struct {
	kubeClient           *kubernetes.Clientset
	namespace            string
	informerFactory      informers.SharedInformerFactory
	statefulSetLister    listersv1.StatefulSetLister
	statefulSetsInformer cache.SharedIndexInformer
	podLister            corelisters.PodLister
	podsInformer         cache.SharedIndexInformer
	maxUnavailable       int
	logger               log.Logger

	// This bool is true if we should trigger a reconcile.
	shouldReconcile atomic.Bool
}

func NewController(kubeClient *kubernetes.Clientset, namespace string, informerFactory informers.SharedInformerFactory, logger log.Logger) *Controller {
	statefulSetsInformer := informerFactory.Apps().V1().StatefulSets()
	podsInformer := informerFactory.Core().V1().Pods()

	c := &Controller{
		kubeClient:           kubeClient,
		informerFactory:      informerFactory,
		statefulSetLister:    statefulSetsInformer.Lister(),
		statefulSetsInformer: statefulSetsInformer.Informer(),
		podLister:            podsInformer.Lister(),
		podsInformer:         podsInformer.Informer(),
		maxUnavailable:       2, // TODO allow to configure via a Config
		logger:               logger,
	}

	// We enqueue a reconcile each time any of the observed StatefulSets change. The UpdateFunc
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

	return c
}

func (c *Controller) Run() error {
	// TODO use correctly (I just did an hack here)
	stopCh := make(chan struct{})
	ctx := context.Background()

	go c.informerFactory.Start(stopCh)

	level.Info(c.logger).Log("msg", "informer cache is synching")
	if ok := cache.WaitForCacheSync(stopCh, c.statefulSetsInformer.HasSynced, c.podsInformer.HasSynced); !ok {
		return errors.New("informer cache has successfully synced")
	}
	level.Info(c.logger).Log("msg", "informer caches has synced")

	for {
		if c.shouldReconcile.CAS(true, false) {
			if err := c.reconcile(ctx); err != nil {
				level.Warn(c.logger).Log("msg", "failed to reconcile", "err", err)

				// We should try to reconcile again.
				c.shouldReconcile.Store(true)
			}
		}

		select {
		case <-stopCh:
			return nil
		case <-time.After(5 * time.Second):
			// Throttle before checking again if we should reconcile.
		}
	}
}

// enqueueReconcile enqueues a request to run a reconcile as soon as possible. If multiple reconcile
// requests are enqueued while
func (c *Controller) enqueueReconcile() {
	c.shouldReconcile.Store(true)
}

func (c *Controller) reconcile(ctx context.Context) error {
	sets, err := c.listStatefulSets(c.namespace, labels.Set{"rollout-group": "ingester"})
	if err != nil {
		return err
	}

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
	// unavailable ingesters in multiple StatefulSets this could lead to an outage, so we want pods to
	// get back to Ready first before proceeding.
	if len(notReadySets) > 1 {
		return fmt.Errorf("%d StatefulSets have some not-Ready pods, skipping reconcile", len(notReadySets))
	}

	// If there's a StatefulSet with not-Ready pods we also want that one to be the first one to reconcile.
	if len(notReadySets) == 1 {
		sets = moveStatefulSetToFirst(sets, notReadySets[0])
	}

	for _, sts := range sets {
		ongoing, err := c.rolloutStatefulSet(ctx, sts)
		if err != nil {
			level.Warn(c.logger).Log("msg", "an error occurred while updating StatefulSet", "statefulset", sts.Name, "err", err)

			// Do not continue with other StatefulSets because this StatefulSet
			// is expected to be successfully updated before proceeding with next ones.
			break
		}

		if ongoing {
			// Do not continue with other StatefulSets because this StatefulSet
			// update is still ongoing.
			break
		}
	}

	return nil
}

func (c *Controller) listStatefulSets(namespace string, lbls labels.Set) ([]*v1.StatefulSet, error) {
	sets, err := c.statefulSetLister.StatefulSets(namespace).List(lbls.AsSelector())
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

func (c *Controller) listPods(namespace string, sel labels.Selector) ([]*corev1.Pod, error) {
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

func (c *Controller) rolloutStatefulSet(ctx context.Context, sts *v1.StatefulSet) (bool, error) {
	level.Debug(c.logger).Log("msg", "checking if statefulset needs to be updated", "statefulset", sts.Name)

	podsToUpdate, err := c.podsToUpdate(sts)
	if err != nil {
		return false, errors.Wrap(err, "failed to get pods to rollout")
	}

	if len(podsToUpdate) > 0 {
		numNotReady := int(sts.Status.Replicas - sts.Status.ReadyReplicas)

		// Compute the number of pods we should update, honoring the configured maxUnavailable.
		numPods := max(0, min(
			c.maxUnavailable-numNotReady, // No more than the configured maxUnavailable (including any not-Ready pod).
			len(podsToUpdate),            // No more than the total number of pods that needs to be updated.
		))

		if numPods == 0 {
			level.Info(c.logger).Log(
				"msg", "statefulset has some pods to be updated but maxUnavailable pods has been reached",
				"statefulset", sts.Name,
				"pods_to_update", len(podsToUpdate),
				"replicas", sts.Status.Replicas,
				"ready_replicas", sts.Status.ReadyReplicas,
				"max_unavailable", c.maxUnavailable)

			return true, nil
		}

		for _, pod := range podsToUpdate[:numPods] {
			level.Info(c.logger).Log("msg", fmt.Sprintf("updating pod %s", pod.Name))
			// TODO delete for real
			//if err := c.kubeClient.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil {
			//	return false, errors.Wrapf(err, "failed to restart pod %s", pod.Name)
			//}
		}

		return true, nil
	}

	// Ensure all pods in this StatefulSet are Ready, otherwise we consider a rollout is in progress
	// (in any case, it's not safe to proceed with other StatefulSets).
	if sts.Status.Replicas != sts.Status.ReadyReplicas {
		return true, nil
	}

	// At this point there are no pods to update, so we can update the currentRevision in the StatefulSet.
	// When the StatefulSet update strategy is RollingUpdate this is done automatically by the controller,
	// when when it's OnDelete (our case) then it's our responsibility to update it once done.
	sts.Status.CurrentReplicas = sts.Status.UpdatedReplicas
	sts.Status.CurrentRevision = sts.Status.UpdateRevision

	level.Debug(c.logger).Log("msg", "updating StatefulSet revision", "old_current_revision", sts.Status.CurrentReplicas, "new_current_revision", sts.Status.UpdateRevision)
	if sts, err = c.kubeClient.AppsV1().StatefulSets(sts.Namespace).UpdateStatus(ctx, sts, metav1.UpdateOptions{}); err != nil {
		return false, errors.Wrapf(err, "failed to update StatefulSet %s", sts.Name)
	}
	level.Info(c.logger).Log("msg", "updated StatefulSet revision", "old_current_revision", sts.Status.CurrentReplicas, "new_current_revision", sts.Status.UpdateRevision)

	return false, nil
}

func (c *Controller) podsToUpdate(sts *v1.StatefulSet) ([]*corev1.Pod, error) {
	var (
		currRev   = sts.Status.CurrentRevision
		updateRev = sts.Status.UpdateRevision
	)

	if currRev == "" {
		return nil, errors.New("currentRevision is empty")
	} else if updateRev == "" {
		return nil, errors.New("updateRevision is empty")
	} else if currRev == updateRev {
		// No pods to update because current and update revision are the same
		return nil, nil
	}

	// Get any pods whose revision doesn't match the StatefulSet's updateRevision
	// and so it means they still need to be updated.
	podSelector := labels.NewSelector().Add(
		mustNewLabelsRequirement("controller-revision-hash", selection.NotEquals, []string{updateRev}),
		mustNewLabelsRequirement("name", selection.Equals, []string{sts.Spec.Template.Labels["name"]}),
	)

	pods, err := c.listPods(sts.Namespace, podSelector)
	if err != nil {
		return nil, err
	}

	// Sort pods in order to provide a deterministic behaviour.
	sortPods(pods)

	return pods, nil
}
