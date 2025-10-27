package controller

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel"
	"go.uber.org/atomic"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listersv1 "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/scale"
	"k8s.io/client-go/tools/cache"

	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/grafana/rollout-operator/pkg/util"
)

const (
	// How frequently informers should resync. This is also the frequency at which
	// the operator reconciles even if no changes are made to the watched resources.
	informerSyncInterval = 5 * time.Minute
)

var tracer = otel.Tracer("pkg/controller")

type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type ZPDBEvictionController interface {
	MarkPodAsDeleted(ctx context.Context, namespace string, podName string, source string) error
}

type RolloutController struct {
	kubeClient           kubernetes.Interface
	clusterDomain        string
	namespace            string
	reconcileInterval    time.Duration
	statefulSetsFactory  informers.SharedInformerFactory
	statefulSetLister    listersv1.StatefulSetLister
	statefulSetsInformer cache.SharedIndexInformer
	podsFactory          informers.SharedInformerFactory
	podLister            corelisters.PodLister
	podsInformer         cache.SharedIndexInformer
	restMapper           meta.RESTMapper
	scaleClient          scale.ScalesGetter
	dynamicClient        dynamic.Interface
	httpClient           httpClient
	logger               log.Logger

	zpdbController ZPDBEvictionController

	// This bool is true if we should trigger a reconcile.
	shouldReconcile atomic.Bool

	// Used to signal when the controller should stop.
	stopCh chan struct{}

	// Metrics.
	groupReconcileTotal       *prometheus.CounterVec
	groupReconcileFailed      *prometheus.CounterVec
	groupReconcileDuration    *prometheus.HistogramVec
	groupReconcileLastSuccess *prometheus.GaugeVec

	// Keep track of discovered rollout groups. We use this information to delete metrics
	// related to rollout groups that have been decommissioned.
	discoveredGroups map[string]struct{}
}

func NewRolloutController(kubeClient kubernetes.Interface, restMapper meta.RESTMapper, scaleClient scale.ScalesGetter, dynamic dynamic.Interface, clusterDomain string, namespace string, client httpClient, reconcileInterval time.Duration, reg prometheus.Registerer, logger log.Logger, zpdbController ZPDBEvictionController) *RolloutController {
	namespaceOpt := informers.WithNamespace(namespace)

	// Initialise the StatefulSet informer to restrict the returned StatefulSets to only the ones
	// having the rollout group label. Only these StatefulSets are managed by this operator.
	statefulSetsSel := labels.NewSelector().Add(util.MustNewLabelsRequirement(config.RolloutGroupLabelKey, selection.Exists, nil)).String()
	statefulSetsSelOpt := informers.WithTweakListOptions(func(options *metav1.ListOptions) {
		options.LabelSelector = statefulSetsSel
	})

	statefulSetsFactory := informers.NewSharedInformerFactoryWithOptions(kubeClient, informerSyncInterval, namespaceOpt, statefulSetsSelOpt)
	statefulSetsInformer := statefulSetsFactory.Apps().V1().StatefulSets()
	podsFactory := informers.NewSharedInformerFactoryWithOptions(kubeClient, informerSyncInterval, namespaceOpt)
	podsInformer := podsFactory.Core().V1().Pods()

	c := &RolloutController{
		kubeClient:           kubeClient,
		clusterDomain:        clusterDomain,
		namespace:            namespace,
		reconcileInterval:    reconcileInterval,
		statefulSetsFactory:  statefulSetsFactory,
		statefulSetLister:    statefulSetsInformer.Lister(),
		statefulSetsInformer: statefulSetsInformer.Informer(),
		podsFactory:          podsFactory,
		podLister:            podsInformer.Lister(),
		podsInformer:         podsInformer.Informer(),
		restMapper:           restMapper,
		scaleClient:          scaleClient,
		dynamicClient:        dynamic,
		httpClient:           client,
		zpdbController:       zpdbController,
		logger:               logger,
		stopCh:               make(chan struct{}),
		discoveredGroups:     map[string]struct{}{},
		groupReconcileTotal: promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
			Name: "rollout_operator_group_reconciles_total",
			Help: "Total number of reconciles started for a specific rollout group.",
		}, []string{"rollout_group"}),
		groupReconcileFailed: promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
			Name: "rollout_operator_group_reconciles_failed_total",
			Help: "Total number of reconciles failed for a specific rollout group.",
		}, []string{"rollout_group"}),
		groupReconcileDuration: promauto.With(reg).NewHistogramVec(prometheus.HistogramOpts{
			Name:    "rollout_operator_group_reconcile_duration_seconds",
			Help:    "Total time spent running a reconcile for a specific rollout group.",
			Buckets: prometheus.DefBuckets,
		}, []string{"rollout_group"}),
		groupReconcileLastSuccess: promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
			Name: "rollout_operator_last_successful_group_reconcile_timestamp_seconds",
			Help: "Timestamp of the last successful reconcile for a specific rollout group.",
		}, []string{"rollout_group"}),
	}

	return c
}

// Init the controller.
func (c *RolloutController) Init() error {
	// We enqueue a reconcile request each time any of the observed StatefulSets are updated. The UpdateFunc
	// is also called every sync period even if no changes occurred.
	_, err := c.statefulSetsInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.onAdded,
		UpdateFunc: c.onUpdated,
		DeleteFunc: c.onDeleted,
	})
	if err != nil {
		return err
	}

	// We enqueue a reconcile request each time any of the observed Pods are updated. Reason is that we may
	// need to proceed with the rollout whenever the state of Pods change (eg. they become Ready).
	_, err = c.podsInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(old, new interface{}) {
			c.enqueueReconcile()
		},
	})
	if err != nil {
		return err
	}

	// Start informers.
	go c.statefulSetsFactory.Start(c.stopCh)
	go c.podsFactory.Start(c.stopCh)

	// Wait until all informer caches have been synced.
	level.Info(c.logger).Log("msg", "informer caches are syncing")
	if ok := cache.WaitForCacheSync(c.stopCh, c.statefulSetsInformer.HasSynced, c.podsInformer.HasSynced); !ok {
		return errors.New("informer caches failed to sync")
	}
	level.Info(c.logger).Log("msg", "informer caches have synced")

	return nil
}

func (c *RolloutController) onAdded(obj interface{}) {
	sts, isStatefulSet := obj.(*v1.StatefulSet)
	if isStatefulSet {
		level.Debug(c.logger).Log(
			"msg", "observed StatefulSet added",
			"name", sts.Name,
			"namespace", sts.Namespace,
			"replicas", sts.Spec.Replicas,
			"generation", sts.Generation,
			"creation_timestamp", sts.CreationTimestamp,
		)
	}

	c.enqueueReconcile()
}

func (c *RolloutController) onUpdated(old, new interface{}) {
	oldSts, oldIsStatefulSet := old.(*v1.StatefulSet)
	newSts, newIsStatefulSet := new.(*v1.StatefulSet)
	if oldIsStatefulSet && newIsStatefulSet && oldSts.Generation != newSts.Generation {
		level.Debug(c.logger).Log(
			"msg", "observed StatefulSet updated",
			"name", oldSts.Name,
			"namespace", oldSts.Namespace,
			"old_replicas", oldSts.Spec.Replicas,
			"new_replicas", newSts.Spec.Replicas,
			"old_generation", oldSts.Generation,
			"new_generation", newSts.Generation,
		)
	}

	c.enqueueReconcile()
}

func (c *RolloutController) onDeleted(obj interface{}) {
	sts, isStatefulSet := obj.(*v1.StatefulSet)
	if isStatefulSet {
		level.Debug(c.logger).Log(
			"msg", "observed StatefulSet deleted",
			"name", sts.Name,
			"namespace", sts.Namespace,
			"replicas", sts.Spec.Replicas,
			"generation", sts.Generation,
		)
	}
}

// Run runs the controller and blocks until Stop() is called.
func (c *RolloutController) Run() {
	ctx := context.Background()

	for {
		if c.shouldReconcile.CompareAndSwap(true, false) {
			if err := c.reconcile(ctx); err != nil {
				level.Warn(c.logger).Log("msg", "reconcile failed", "err", err)

				// We should try to reconcile again.
				c.shouldReconcile.Store(true)
			}
		}

		select {
		case <-c.stopCh:
			return
		case <-time.After(c.reconcileInterval):
			// Throttle before checking again if we should reconcile.
		}
	}
}

// Stop the controller.
func (c *RolloutController) Stop() {
	close(c.stopCh)
}

// enqueueReconcile requests to run a reconcile at the next interval.
func (c *RolloutController) enqueueReconcile() {
	c.shouldReconcile.Store(true)
}

func (c *RolloutController) reconcile(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "RolloutController.reconcile()")
	defer span.End()

	level.Info(c.logger).Log("msg", "reconcile started")

	sets, err := c.listStatefulSetsWithRolloutGroup()
	if err != nil {
		return err
	}

	// Group statefulsets by the rollout group label. Each group will be reconciled independently.
	groups := util.GroupStatefulSetsByLabel(sets, config.RolloutGroupLabelKey)
	var reconcileErrs error
	for groupName, groupSets := range groups {
		if err := c.reconcileStatefulSetsGroup(ctx, groupName, groupSets); err != nil {
			reconcileErrs = multierror.Append(reconcileErrs, err)
		}
	}

	if reconcileErrs != nil {
		return reconcileErrs
	}

	c.deleteMetricsForDecommissionedGroups(groups)

	level.Info(c.logger).Log("msg", "reconcile done")
	return nil
}

func (c *RolloutController) reconcileStatefulSetsGroup(ctx context.Context, groupName string, sets []*v1.StatefulSet) (returnErr error) {
	// Track metrics about the reconcile operation. We always get the failed counter and
	// last successful gauge so that they get created the first time with the default zero
	// value the first time.
	c.groupReconcileTotal.WithLabelValues(groupName).Inc()
	durationTimer := prometheus.NewTimer(c.groupReconcileDuration.WithLabelValues(groupName))
	failed := c.groupReconcileFailed.WithLabelValues(groupName)
	lastSuccess := c.groupReconcileLastSuccess.WithLabelValues(groupName)
	defer func() {
		durationTimer.ObserveDuration()
		if returnErr != nil {
			failed.Inc()
		} else {
			lastSuccess.SetToCurrentTime()
		}
	}()

	// Sort StatefulSets to provide a deterministic behaviour.
	util.SortStatefulSets(sets)

	// Adjust the number of replicas for each StatefulSet in the group if desired. If the number of
	// replicas of any StatefulSet was adjusted, return early in order to guarantee each STS model is
	// up-to-date.
	updated, err := c.adjustStatefulSetsGroupReplicas(ctx, groupName, sets)
	if err != nil {
		level.Warn(c.logger).Log("msg", "unable to adjust desired replicas of StatefulSet", "group", groupName, "err", err)
	}
	if err == nil && updated {
		level.Debug(c.logger).Log("msg", "ending reconcile early due to updated StatefulSet replicas", "group", groupName)
		return nil
	}

	// Ensure all StatefulSets have OnDelete update strategy. Otherwise, we're not able to guarantee
	// for only 1 StatefulSet to be updated at a time.
	for _, sts := range sets {
		if sts.Spec.UpdateStrategy.Type != v1.OnDeleteStatefulSetStrategyType {
			return fmt.Errorf("StatefulSet %s has %s update strategy while %s is expected, skipping reconcile", sts.Name, sts.Spec.UpdateStrategy.Type, v1.OnDeleteStatefulSetStrategyType)
		}
	}

	// Find StatefulSets with some not-Ready pods.
	notReadySets := make([]*v1.StatefulSet, 0, len(sets))
	for _, sts := range sets {
		hasNotReadyPods, err := c.hasStatefulSetNotReadyPods(sts)
		if err != nil {
			return errors.Wrapf(err, "unable to check if StatefulSet %s has not ready pods", sts.Name)
		}

		if hasNotReadyPods {
			notReadySets = append(notReadySets, sts)
		}
	}

	// Ensure there are not 2+ StatefulSets with not-Ready pods. If there are, we shouldn't proceed
	// rolling out pods and we should wait until these pods are Ready. The reason is that if there are
	// unavailable pods in multiple StatefulSets, this could lead to an outage, so we want pods to
	// get back to Ready first before proceeding.
	if len(notReadySets) > 1 {
		// Do not return error because it's not an actionable error with regards to the operator behaviour.
		level.Warn(c.logger).Log("msg", "StatefulSets have some not-Ready pods, skipping reconcile", "not_ready_statefulsets", len(notReadySets))
		return nil
	}

	// If there's a StatefulSet with not-Ready pods we also want that one to be the first one to reconcile.
	if len(notReadySets) == 1 {
		level.Info(c.logger).Log("msg", "a StatefulSet has some not-Ready pods, reconcile it first", "statefulset", notReadySets[0].Name)
		sets = util.MoveStatefulSetToFront(sets, notReadySets[0])
	}

	for _, sts := range sets {
		ongoing, err := c.updateStatefulSetPods(ctx, sts)
		if err != nil {
			// Do not continue with other StatefulSets because this StatefulSet
			// is expected to be successfully updated before proceeding.
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

// adjustStatefulSetsGroupReplicas examines each StatefulSet and adjusts the number of replicas if desired.
// The method returns "true" only if the number of replicas in any StatefulSet was adjusted.
func (c *RolloutController) adjustStatefulSetsGroupReplicas(ctx context.Context, groupName string, sets []*v1.StatefulSet) (bool, error) {
	updated, err := c.adjustStatefulSetsGroupReplicasToFollowLeader(ctx, groupName, sets)
	if err != nil || updated {
		return updated, err
	}

	return c.adjustStatefulSetsGroupReplicasToMirrorResource(ctx, groupName, sets, c.clusterDomain, c.httpClient)
}

// adjustStatefulSetsGroupReplicasToFollowLeader examines each StatefulSet and adjusts the number of replicas if desired,
// based on leader-replicas, and minimum time between zone downscales.
// The method returns "true" early if the number of replicas in any StatefulSet was adjusted.
func (c *RolloutController) adjustStatefulSetsGroupReplicasToFollowLeader(ctx context.Context, groupName string, sets []*v1.StatefulSet) (bool, error) {
	// Return early no matter what after scaling up or down a single StatefulSet. We do this because
	// we need to be sure the number of replicas or any relevant annotations (like last downscale time)
	// are up-to-date on the models. If we modify the StatefulSet, they no longer are.
	for _, sts := range sets {
		currentReplicas := *sts.Spec.Replicas
		desiredReplicas, err := desiredStsReplicas(groupName, sts, sets, c.logger)
		if err != nil {
			return false, err
		}

		if desiredReplicas > currentReplicas {
			level.Info(c.logger).Log(
				"msg", "scaling up statefulset to match leader",
				"group", groupName,
				"name", sts.GetName(),
				"replicas", desiredReplicas,
			)

			if err := c.patchStatefulSetSpecReplicas(ctx, sts, desiredReplicas); err != nil {
				// If the patch failed, don't consider the statefulsets changed
				return false, err
			}

			return true, nil
		} else if desiredReplicas < currentReplicas {
			level.Info(c.logger).Log(
				"msg", "scaling down statefulset to match leader",
				"group", groupName,
				"name", sts.GetName(),
				"replicas", desiredReplicas,
			)

			if err := c.patchStatefulSetSpecReplicas(ctx, sts, desiredReplicas); err != nil {
				// If the patch failed, don't consider the statefulsets changed
				return false, err
			}

			return true, nil
		}

		// No change in the number of replicas: don't log because this will be the result most of the time.
	}

	return false, nil
}

func (c *RolloutController) listStatefulSetsWithRolloutGroup() ([]*v1.StatefulSet, error) {
	// List "all" StatefulSets matching the label matcher configured in the informer (so only
	// the StatefulSets having a rollout group label).
	sets, err := c.statefulSetLister.StatefulSets(c.namespace).List(labels.Everything())
	if err != nil {
		return nil, errors.Wrap(err, "failed to list StatefulSets")
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

func (c *RolloutController) hasStatefulSetNotReadyPods(sts *v1.StatefulSet) (bool, error) {
	// We can quickly check the number of ready replicas reported by the StatefulSet.
	// If they don't match the total number of replicas, then we're sure there are some
	// not ready pods.
	if sts.Status.Replicas != sts.Status.ReadyReplicas {
		return true, nil
	}

	// First we check that we see at least the number of pods desired by the Replicas field.
	// Sometimes it takes a while until pods are created after terminating them.
	// We consider the missing pods as not ready.
	pods, err := c.listPodsByStatefulSet(sts)
	if err != nil {
		return false, err
	}

	if len(pods) < int(sts.Status.Replicas) {
		level.Info(c.logger).Log(
			"msg", "StatefulSet status is reporting all pods ready, but the rollout operator found less pods than expected",
			"statefulset", sts.Name,
			"expected_replicas", sts.Status.Replicas,
			"found_pods", len(pods),
		)
		return true, nil
	}

	// The number of ready replicas reported by the StatefulSet matches the total number of
	// replicas. However, there's still no guarantee that all pods are running. For example,
	// a terminating pod (which we don't consider "ready") may have not yet failed the
	// readiness probe for the consecutive number of times required to switch its status
	// to not-ready. For this reason, we list all StatefulSet pods and check them one-by-one.
	notReadyPods := notRunningAndReady(pods)
	if len(notReadyPods) == 0 {
		return false, nil
	}

	// Log which pods have been detected as not-ready. This may be useful for debugging.
	level.Info(c.logger).Log(
		"msg", "StatefulSet status is reporting all pods ready, but the rollout operator has found some not-Ready pods",
		"statefulset", sts.Name,
		"not_ready_pods", strings.Join(util.PodNames(notReadyPods), " "),
	)

	return true, nil
}

func (c *RolloutController) listPodsByStatefulSet(sts *v1.StatefulSet) ([]*corev1.Pod, error) {
	// Select all pods belonging to the input StatefulSet.
	podsSelector := labels.NewSelector().Add(
		util.MustNewLabelsRequirement("name", selection.Equals, []string{sts.Spec.Template.Labels["name"]}),
	)

	pods, err := c.listPods(podsSelector)
	if err != nil {
		return nil, err
	}
	return pods, nil
}

func notRunningAndReady(pods []*corev1.Pod) []*corev1.Pod {
	// Build a list of not-ready ones.
	// We don't pre-allocate this list because most of the time we expect all pods are running and ready.
	var notReady []*corev1.Pod
	for _, pod := range pods {
		if !util.IsPodRunningAndReady(pod) {
			notReady = append(notReady, pod)
		}
	}

	// Sort pods in order to provide a deterministic behaviour.
	util.SortPods(notReady)
	return notReady
}

// listPods returns pods matching the provided labels selector. Please remember to call
// DeepCopy() on the returned pods before doing any change.
func (c *RolloutController) listPods(sel labels.Selector) ([]*corev1.Pod, error) {
	pods, err := c.podLister.Pods(c.namespace).List(sel)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list Pods")
	}

	return pods, nil
}

func (c *RolloutController) updateStatefulSetPods(ctx context.Context, sts *v1.StatefulSet) (bool, error) {
	level.Debug(c.logger).Log("msg", "reconciling StatefulSet", "statefulset", sts.Name)

	podsToUpdate, err := c.podsNotMatchingUpdateRevision(sts)
	if err != nil {
		return false, errors.Wrap(err, "failed to get pods to update")
	}

	if len(podsToUpdate) > 0 {
		maxUnavailable := getMaxUnavailableForStatefulSet(sts, c.logger)
		numNotReady := int(sts.Status.Replicas - sts.Status.ReadyReplicas)

		// Compute the number of pods we should update, honoring the configured maxUnavailable.
		numPods := max(0, min(
			maxUnavailable-numNotReady, // No more than the configured maxUnavailable (including not-Ready pods).
			len(podsToUpdate),          // No more than the total number of pods that need to be updated.
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
			// Skip if the pod is terminating. Since "Terminating" is not a pod Phase, we can infer it by checking
			// if the pod is in the Running phase but the deletionTimestamp has been set (kubectl does something
			// similar too).
			if pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp != nil {
				level.Debug(c.logger).Log("msg", fmt.Sprintf("waiting for pod %s to be terminated", pod.Name))
				continue
			}

			// Use the ZPDB to determine if this pod delete is allowed.
			// The ZPDB serializes requests from this controller and from any incoming voluntary evictions.
			// For each request, a full set of tests is performed to confirm the state of all pods in the ZPDB scope.
			// This ensures that if any voluntary evictions have been allowed since this reconcile loop commenced that
			// the latest information is used to determine the zone/partition disruption level.
			// If this request returns without error, the pod will be placed into the ZPDB pod eviction cache and will
			// be considered as not ready until it either restarts or is expired from the cache.
			err := c.zpdbController.MarkPodAsDeleted(ctx, pod.Namespace, pod.Name, "rollout-controller")
			if err != nil {
				// Skip this pod. The reconcile loop regularly runs and this pod will have an opportunity to be re-tried.
				// Rather than returning false here and abandoning the rest of the updates until the next reconcile,
				// we allow this update loop to continue. For configurations which have a partition aware ZPDB it is valid
				// to have multiple disruptions as long as there is at least one healthy pod per partition.
				level.Debug(c.logger).Log("msg", "zpdb denied pod deletion", "pod", pod.Name, "reason", err)
				continue
			}

			level.Info(c.logger).Log("msg", "terminating pod (does not violate any relevant ZPDBs)", "pod", pod.Name)
			if err := c.kubeClient.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil {
				return false, errors.Wrapf(err, "failed to delete pod %s", pod.Name)
			}
		}

		return true, nil
	}

	// Ensure all pods in this StatefulSet are Ready, otherwise we consider a rollout is in progress
	// (in any case, it's not safe to proceed with other StatefulSets).
	if hasNotReadyPods, err := c.hasStatefulSetNotReadyPods(sts); err != nil {
		return true, errors.Wrapf(err, "unable to check if StatefulSet %s has not ready pods", sts.Name)
	} else if hasNotReadyPods {
		level.Info(c.logger).Log(
			"msg", "StatefulSet pods are all updated but StatefulSet has some not-Ready replicas",
			"statefulset", sts.Name)

		return true, nil
	}

	// At this point there are no pods to update, so we can update the currentRevision in the StatefulSet.
	// When the StatefulSet update strategy is RollingUpdate this is done automatically by the controller,
	// but when it's OnDelete (our case) then it's our responsibility to update it once done.
	if sts.Status.CurrentRevision != sts.Status.UpdateRevision {
		oldRev := sts.Status.CurrentRevision
		sts.Status.CurrentRevision = sts.Status.UpdateRevision

		level.Debug(c.logger).Log("msg", "updating StatefulSet current revision", "old_current_revision", oldRev, "new_current_revision", sts.Status.UpdateRevision)
		if sts, err = c.kubeClient.AppsV1().StatefulSets(sts.Namespace).UpdateStatus(ctx, sts, metav1.UpdateOptions{}); err != nil {
			return false, errors.Wrapf(err, "failed to update StatefulSet %s", sts.Name)
		}
		level.Info(c.logger).Log("msg", "updated StatefulSet current revision", "old_current_revision", oldRev, "new_current_revision", sts.Status.UpdateRevision)
	}

	return false, nil
}

func (c *RolloutController) podsNotMatchingUpdateRevision(sts *v1.StatefulSet) ([]*corev1.Pod, error) {
	var (
		currRev   = sts.Status.CurrentRevision
		updateRev = sts.Status.UpdateRevision
	)

	// Do NOT introduce a short circuit if "currRev == updateRev". The reason is that if a change
	// is rolled back in the StatefulSet to the previous version, the updateRev == currRev but
	// its pods may still run the previous updateRev. We need to check pods to be 100% sure.
	if currRev == "" {
		return nil, errors.New("currentRevision is empty")
	} else if updateRev == "" {
		return nil, errors.New("updateRevision is empty")
	}

	// Get any pods which revision doesn't match the StatefulSet's updateRevision
	// and so it means they still need to be updated.
	podsSelector := labels.NewSelector().Add(
		util.MustNewLabelsRequirement(v1.ControllerRevisionHashLabelKey, selection.NotEquals, []string{updateRev}),
		util.MustNewLabelsRequirement("name", selection.Equals, []string{sts.Spec.Template.Labels["name"]}),
	)

	pods, err := c.listPods(podsSelector)
	if err != nil {
		return nil, err
	}

	// Sort pods in order to provide a deterministic behaviour.
	util.SortPods(pods)

	return pods, nil
}

func (c *RolloutController) deleteMetricsForDecommissionedGroups(groups map[string][]*v1.StatefulSet) {
	// Delete metrics for decommissioned groups.
	for name := range c.discoveredGroups {
		if _, ok := groups[name]; ok {
			continue
		}

		c.groupReconcileTotal.DeleteLabelValues(name)
		c.groupReconcileFailed.DeleteLabelValues(name)
		c.groupReconcileDuration.DeleteLabelValues(name)
		c.groupReconcileLastSuccess.DeleteLabelValues(name)
		delete(c.discoveredGroups, name)
	}

	// Update the discovered groups.
	for name := range groups {
		c.discoveredGroups[name] = struct{}{}
	}
}

func (c *RolloutController) patchStatefulSetSpecReplicas(ctx context.Context, sts *v1.StatefulSet, replicas int32) error {
	patch := fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas)
	_, err := c.kubeClient.AppsV1().StatefulSets(c.namespace).Patch(ctx, sts.GetName(), types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}
