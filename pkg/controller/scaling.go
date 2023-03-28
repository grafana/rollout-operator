package controller

import (
	"context"
	"fmt"

	"github.com/go-kit/log/level"
	"github.com/pkg/errors"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	kubeerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	rolloutoperator "github.com/grafana/rollout-operator/pkg/apis/rolloutoperator/v1alpha1"
)

func (c *RolloutController) enqueueIngesterAutoScaler(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		level.Warn(c.logger).Log("msg", "getting MultiZoneIngesterAutoScaler from cache failed", "err", err)
		return
	}

	c.autoScalingQueue.Add(key)
}

// ingesterAutoScalingWorker processes jobs from c.autoScalingQueue.
func (c *RolloutController) ingesterAutoScalingWorker(ctx context.Context) {
	level.Info(c.logger).Log("msg", "reconciling ingester scaling custom resources...")

	for c.processNextWorkItem(ctx) {
	}

	return
}

func (c *RolloutController) processNextWorkItem(ctx context.Context) bool {
	obj, shutdown := c.autoScalingQueue.Get()
	if shutdown {
		return false
	}

	// We call Done here so the workqueue knows we have finished
	// processing this item. We also must remember to call Forget if we
	// do not want this work item being re-queued. For example, we do
	// not call Forget if a transient error occurs, instead the item is
	// put back on the workqueue and attempted again after a back-off
	// period.
	defer c.autoScalingQueue.Done(obj)
	var key string
	var ok bool
	// We expect strings to come off the workqueue. These are of the
	// form namespace/name. We do this as the delayed nature of the
	// workqueue means the items in the informer cache may actually be
	// more up to date that when the item was initially put onto the
	// workqueue.
	if key, ok = obj.(string); !ok {
		// As the item in the workqueue is actually invalid, we call
		// Forget here else we'd go into a loop of attempting to
		// process a work item that is invalid.
		c.autoScalingQueue.Forget(obj)
		level.Warn(c.logger).Log("msg", fmt.Sprintf("expected string in autoScalingQueue, but got %#v", obj))
		return true
	}
	// Run the syncHandler, passing it the namespace/name string of the
	// Foo resource to be synced.
	if err := c.syncHandler(ctx, key); err != nil {
		// Put the item back on the workqueue to handle any transient errors.
		c.autoScalingQueue.AddRateLimited(key)
		level.Warn(c.logger).Log("msg", fmt.Sprintf("error syncing '%s', requeuing", key), "err", err)
		return true
	}
	// Finally, if no error occurs we Forget this item so it does not
	// get queued again until another change happens.
	c.autoScalingQueue.Forget(obj)
	level.Info(c.logger).Log("msg", "successfully synced", "resource", key)

	return true
}

// syncHandler compares the actual state with the desired, and attempts to
// reconcile the two. It then updates the Status block of the MultiZoneIngesterAutoScaler
// resource with the current status.
func (c *RolloutController) syncHandler(ctx context.Context, key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		level.Warn(c.logger).Log("msg", "invalid resource", "key", key)
		return nil
	}

	// Get the MultiZoneIngesterAutoScaler resource with this namespace/name
	rsrc, err := c.ingesterAutoScalerLister.MultiZoneIngesterAutoScalers(namespace).Get(name)
	if err != nil {
		// The resource may no longer exist, in which case we stop
		// processing.
		if kubeerrors.IsNotFound(err) {
			level.Warn(c.logger).Log("msg", "resource in work queue no longer exists", "key", key)
			return nil
		}

		return err
	}

	const hpa1Name = "rollout-operator-hpa-ingester-zone-a"
	hpa1, err := c.hpaLister.HorizontalPodAutoscalers(c.namespace).Get(hpa1Name)
	if err != nil {
		// If the resource doesn't exist, we'll create it
		if kubeerrors.IsNotFound(err) {
			hpa := newHPA(rsrc, hpa1Name, "a")
			level.Info(c.logger).Log("msg", "creating missing zone A HPA", "name", hpa.Name,
				"namespace", hpa.Namespace)
			hpa1, err = c.kubeClient.AutoscalingV2().HorizontalPodAutoscalers(c.namespace).Create(
				ctx, hpa, metav1.CreateOptions{})
			err = errors.Wrapf(err, "failed to create HPA %s", hpa1Name)
		} else {
			err = errors.Wrapf(err, "couldn't get HPA %s", hpa1Name)
		}

		// If an error occurs during Get/Create, we'll requeue the item so we can
		// attempt processing again later. This could have been caused by a
		// temporary network failure, or any other transient reason.
		return err
	}

	if !metav1.IsControlledBy(hpa1, rsrc) {
		return fmt.Errorf(fmt.Sprintf("resource %q already exists and is not managed by MultiZoneIngesterAutoScaler", hpa1.Name))
	}

	// TODO: Reconcile HPA state

	// TODO: Deal with HPAs for zones B and C

	return c.updateScalerStatus(ctx, rsrc, hpa1)
}

func (c *RolloutController) updateScalerStatus(ctx context.Context, scaler *rolloutoperator.MultiZoneIngesterAutoScaler,
	hpa *autoscalingv2.HorizontalPodAutoscaler) error {
	// NEVER modify objects from the store. It's a read-only, local cache.
	scalerCopy := scaler.DeepCopy()
	// TODO: Update resource status
	// scalerCopy.Status.AvailableReplicas = deployment.Status.AvailableReplicas
	// If the CustomResourceSubresources feature gate is not enabled,
	// we must use Update instead of UpdateStatus to update the Status block of the Foo resource.
	// UpdateStatus will not allow changes to the Spec of the resource,
	// which is ideal for ensuring nothing other than resource status has been updated.
	_, err := c.customClient.RolloutoperatorV1alpha1().MultiZoneIngesterAutoScalers(scaler.Namespace).UpdateStatus(ctx, scalerCopy, metav1.UpdateOptions{})
	return errors.Wrap(err, "update MultiZoneIngesterAutoScaler status")
}

// newHPA creates a new HPA for a MultiZoneIngesterAutoScaler resource. It also sets
// the appropriate OwnerReferences on the resource so handleObject can discover
// the owning resource.
func newHPA(owner *rolloutoperator.MultiZoneIngesterAutoScaler, name, zone string) *autoscalingv2.HorizontalPodAutoscaler {
	/*
		labels := map[string]string{
			"controller": owner.Name,
		}
	*/
	// TODO: Don't hardcode number of replicas
	// Temporarily set min and max to the same, to avoid scaling
	minReplicas := int32(5)
	maxReplicas := int32(5)
	avgUtilization := int32(90)
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: owner.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(owner, rolloutoperator.SchemeGroupVersion.WithKind("MultiZoneIngesterAutoScaler")),
			},
			Labels: map[string]string{
				"controller": owner.Name,
			},
		},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				Kind: "StatefulSet",
				Name: fmt.Sprintf("ingester-zone-%s", zone),
			},
			MinReplicas: &minReplicas,
			MaxReplicas: maxReplicas,
			Metrics: []autoscalingv2.MetricSpec{
				{
					Type: "Resource",
					Resource: &autoscalingv2.ResourceMetricSource{
						Name: "cpu",
						Target: autoscalingv2.MetricTarget{
							Type:               "Utilization",
							AverageUtilization: &avgUtilization,
						},
					},
				},
				{
					Type: "Resource",
					Resource: &autoscalingv2.ResourceMetricSource{
						Name: "memory",
						Target: autoscalingv2.MetricTarget{
							Type:               "Utilization",
							AverageUtilization: &avgUtilization,
						},
					},
				},
			},
		},
	}
}

// handleHPA will take any resource implementing metav1.Object and attempt
// to find the MultiZoneIngesterAutoScaler resource that owns it.
// If successful, it enqueues that MultiZoneIngesterAutoScaler resource for processing,
// otherwise it gets skipped.
func (c *RolloutController) handleHPA(obj interface{}) {
	var object metav1.Object
	var ok bool
	if object, ok = obj.(metav1.Object); !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			level.Warn(c.logger).Log("msg", "error decoding object, invalid type")
			return
		}
		object, ok = tombstone.Obj.(metav1.Object)
		if !ok {
			level.Warn(c.logger).Log("msg", "error decoding object tombstone, invalid type")
			return
		}
		level.Info(c.logger).Log("msg", "recovered deleted object", "resourceName", object.GetName())
	}

	level.Info(c.logger).Log("msg", "processing object", "object", klog.KObj(object))
	ownerRef := metav1.GetControllerOf(object)
	if ownerRef == nil || ownerRef.Kind != "MultiZoneIngesterAutoScaler" {
		return
	}

	scaler, err := c.ingesterAutoScalerLister.MultiZoneIngesterAutoScalers(object.GetNamespace()).Get(ownerRef.Name)
	if err != nil {
		level.Info(c.logger).Log("msg", "ignoring orphaned object", "object", klog.KObj(object),
			"owner", ownerRef.Name)
		return
	}

	c.enqueueIngesterAutoScaler(scaler)
}
