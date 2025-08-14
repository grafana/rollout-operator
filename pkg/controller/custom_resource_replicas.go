package controller

import (
	"context"
	"fmt"
	"strconv"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	scaleclient "k8s.io/client-go/scale"

	"github.com/grafana/rollout-operator/pkg/config"
)

func (c *RolloutController) adjustStatefulSetsGroupReplicasToMirrorResource(ctx context.Context, groupName string, sets []*appsv1.StatefulSet, clusterDomain string, client httpClient) (bool, error) {
	// Return early no matter what after scaling up or down a single StatefulSet to make sure that rollout-operator
	// works with up-to-date models.
	for _, sts := range sets {
		currentReplicas := *sts.Spec.Replicas

		scaleObj, referenceGVR, referenceName, err := getCustomScaleResourceForStatefulset(ctx, sts, c.restMapper, c.scaleClient)
		if err != nil {
			return false, err
		}
		if scaleObj == nil {
			continue
		}

		referenceResource := fmt.Sprintf("%s/%s", referenceGVR.Resource, referenceName)

		referenceResourceDesiredReplicas := scaleObj.Spec.Replicas
		if currentReplicas == referenceResourceDesiredReplicas {
			updateStatusReplicasOnReferenceResourceIfNeeded(ctx, c.logger, c.dynamicClient, sts, scaleObj, referenceGVR, referenceName, referenceResourceDesiredReplicas)
			cancelDelayedDownscaleIfConfigured(ctx, c.logger, sts, clusterDomain, client, referenceResourceDesiredReplicas)
			// No change in the number of replicas: don't log because this will be the result most of the time.
			continue
		}

		// We're going to change number of replicas on the statefulset.
		// If there is delayed downscale configured on the statefulset, we will first handle delay part, and only if that succeeds,
		// continue with downscaling or upscaling.
		desiredReplicas, err := checkScalingDelay(ctx, c.logger, sts, c.clusterDomain, client, currentReplicas, referenceResourceDesiredReplicas)
		if err != nil {
			level.Warn(c.logger).Log("msg", "not scaling statefulset due to failed scaling delay check",
				"group", groupName,
				"name", sts.GetName(),
				"currentReplicas", currentReplicas,
				"referenceResourceDesiredReplicas", referenceResourceDesiredReplicas,
				"err", err,
			)

			updateStatusReplicasOnReferenceResourceIfNeeded(ctx, c.logger, c.dynamicClient, sts, scaleObj, referenceGVR, referenceName, currentReplicas)
			// If delay has not been reached, we can check next statefulset.
			continue
		}

		logMsg := ""
		if desiredReplicas > currentReplicas {
			logMsg = "scaling up statefulset to match replicas in the reference resource"
		} else if desiredReplicas < currentReplicas {
			logMsg = "scaling down statefulset to computed desired replicas, based on replicas in the reference resource and elapsed downscale delays"
		}

		level.Info(c.logger).Log("msg", logMsg,
			"group", groupName,
			"name", sts.GetName(),
			"currentReplicas", currentReplicas,
			"referenceResourceDesiredReplicas", referenceResourceDesiredReplicas,
			"computedDesiredReplicas", desiredReplicas,
			"referenceResource", referenceResource,
		)

		if err := c.patchStatefulSetSpecReplicas(ctx, sts, desiredReplicas); err != nil {
			return false, err
		}

		updateStatusReplicasOnReferenceResourceIfNeeded(ctx, c.logger, c.dynamicClient, sts, scaleObj, referenceGVR, referenceName, desiredReplicas)
		return true, nil
	}

	return false, nil
}

func getCustomScaleResourceForStatefulset(ctx context.Context, sts *appsv1.StatefulSet, restMapper meta.RESTMapper, scalesGetter scaleclient.ScalesGetter) (*autoscalingv1.Scale, schema.GroupVersionResource, string, error) {
	annotations := sts.GetAnnotations()
	name := annotations[config.RolloutMirrorReplicasFromResourceNameAnnotationKey]
	kind := annotations[config.RolloutMirrorReplicasFromResourceKindAnnotationKey]
	if name == "" || kind == "" {
		return nil, schema.GroupVersionResource{}, "", nil
	}

	apiVersion := annotations[config.RolloutMirrorReplicasFromResourceAPIVersionAnnotationKey]

	targetGV, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return nil, schema.GroupVersionResource{}, "", fmt.Errorf("invalid API version in %s annotation: %v", config.RolloutMirrorReplicasFromResourceAPIVersionAnnotationKey, err)
	}

	targetGK := schema.GroupKind{
		Group: targetGV.Group,
		Kind:  kind,
	}

	reference := fmt.Sprintf("%s/%s", kind, name)

	mappings, err := restMapper.RESTMappings(targetGK)
	if err != nil {
		return nil, schema.GroupVersionResource{}, "", fmt.Errorf("unable to find custom resource mapping for reference resource %s: %v", reference, err)
	}

	scale, gvr, err := scaleForResourceMappings(ctx, sts.Namespace, name, mappings, scalesGetter)
	if err != nil {
		return nil, schema.GroupVersionResource{}, "", fmt.Errorf("failed to query scale subresource for %s: %v", reference, err)
	}

	return scale, gvr, name, nil
}

// copied from https://github.com/kubernetes/kubernetes/blob/3c4512c6ccca066d590a33b6333198b5ed813da2/pkg/controller/podautoscaler/horizontal.go#L1336-L1358
func scaleForResourceMappings(ctx context.Context, namespace, name string, mappings []*meta.RESTMapping, scalesGetter scaleclient.ScalesGetter) (*autoscalingv1.Scale, schema.GroupVersionResource, error) {
	var firstErr error
	for i, mapping := range mappings {
		scale, err := scalesGetter.Scales(namespace).Get(ctx, mapping.Resource.GroupResource(), name, metav1.GetOptions{})
		if err == nil {
			return scale, mapping.Resource, nil
		}

		// if this is the first error, remember it,
		// then go on and try other mappings until we find a good one
		if i == 0 {
			firstErr = err
		}
	}

	// make sure we handle an empty set of mappings
	if firstErr == nil {
		firstErr = fmt.Errorf("unrecognized resource")
	}

	return nil, schema.GroupVersionResource{}, firstErr
}

// updateStatusReplicasOnReferenceResourceIfNeeded makes sure that scaleObject's status.replicas field is up-to-date.
// if update fails, error is logged, but not returned to caller.
func updateStatusReplicasOnReferenceResourceIfNeeded(ctx context.Context, logger log.Logger, dynamicClient dynamic.Interface, sts *appsv1.StatefulSet, scaleObj *autoscalingv1.Scale, gvr schema.GroupVersionResource, resName string, replicas int32) {
	if scaleObj.Status.Replicas == replicas {
		// Nothing to do.
		return
	}

	referenceResource := fmt.Sprintf("%s/%s", gvr.Resource, resName)

	// Add common fields to logger.
	logger = log.With(logger, "name", sts.GetName(), "replicas", replicas, "referenceResource", referenceResource)

	// If annotation is not present, or equals to "true", we update. If annotation equals to "false" or fails to parse, we don't update.
	updateReplicas, ok := sts.Annotations[config.RolloutMirrorReplicasFromResourceWriteBackStatusReplicas]
	if ok {
		update, err := strconv.ParseBool(updateReplicas)
		if err != nil {
			level.Info(logger).Log("msg", "not updating status.replicas on reference resource to match current replicas of statefulset, failed to parse "+config.RolloutMirrorReplicasFromResourceWriteBackStatusReplicas+" annotation", "err", err)
			return
		}
		if !update {
			level.Info(logger).Log("msg", "not updating status.replicas on reference resource to match current replicas of statefulset, updating disabled")
			return
		}
	}

	level.Info(logger).Log("msg", "updating status.replicas on reference resource to match current replicas of statefulset")

	// We need to update status.replicas on the resource (status subresource), not on the scale subresource.
	patch := fmt.Sprintf(`{"status":{"replicas":%d}}`, replicas)
	_, err := dynamicClient.Resource(gvr).Namespace(sts.Namespace).Patch(ctx, resName, types.MergePatchType, []byte(patch), metav1.PatchOptions{}, "status")
	if err != nil {
		level.Warn(logger).Log("msg", "updating status.replicas on reference resource to match current replicas of statefulset failed", "err", err)
	}
}
