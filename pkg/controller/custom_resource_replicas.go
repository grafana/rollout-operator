package controller

import (
	"context"
	"fmt"

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

func getCustomScaleResourceForStatefulset(ctx context.Context, sts *appsv1.StatefulSet, restMapper meta.RESTMapper, scalesGetter scaleclient.ScalesGetter) (*autoscalingv1.Scale, schema.GroupVersionResource, string, error) {
	annotations := sts.GetAnnotations()
	name := annotations[config.RolloutMirrorReplicasFromResourceNameAnnotationKey]
	kind := annotations[config.RolloutMirrorReplicasFromResourceKindAnnotationKey]
	if name == "" || kind == "" {
		return nil, schema.GroupVersionResource{}, "", nil
	}

	apiVersion := annotations[config.RolloutMirrorReplicasFromResourceAPIVersionAnnotationKey]

	reference := fmt.Sprintf("%s/%s/%s", kind, sts.Namespace, name)

	targetGV, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return nil, schema.GroupVersionResource{}, "", fmt.Errorf("invalid API version in %s annotation: %v", config.RolloutMirrorReplicasFromResourceAPIVersionAnnotationKey, err)
	}

	targetGK := schema.GroupKind{
		Group: targetGV.Group,
		Kind:  kind,
	}

	mappings, err := restMapper.RESTMappings(targetGK)
	if err != nil {
		return nil, schema.GroupVersionResource{}, "", fmt.Errorf("unable to determine resource for scale target reference: %v", err)
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

// updateStatusReplicasOnMirroredResourceIfNeeded makes sure that scaleObject's status.replicas field is up-to-date.
func updateStatusReplicasOnMirroredResourceIfNeeded(ctx context.Context, log log.Logger, dynamicClient dynamic.Interface, sts *appsv1.StatefulSet, scaleObj *autoscalingv1.Scale, gvr schema.GroupVersionResource, resName string, replicas int32) error {
	if scaleObj == nil || scaleObj.Status.Replicas == replicas {
		// Nothing to do.
		return nil
	}

	level.Info(log).Log("msg", "updating status.replicas on resource to match current replicas of statefulset", "resource", fmt.Sprintf("%s/%s", gvr.Resource, resName), "name", sts.GetName(), "replicas", replicas)

	// We need to update status.replicas on the resource (status subresource), not on the scale subresource.
	patch := fmt.Sprintf(`{"status":{"replicas":%d}}`, replicas)
	_, err := dynamicClient.Resource(gvr).Namespace(sts.Namespace).Patch(ctx, resName, types.MergePatchType, []byte(patch), metav1.PatchOptions{}, "status")
	if err != nil {
		return fmt.Errorf("failed to update resource %s/%s status.replicas: %w", gvr.Resource, resName, err)
	}
	return nil
}
