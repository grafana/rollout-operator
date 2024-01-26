package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	scaleclient "k8s.io/client-go/scale"

	"github.com/grafana/rollout-operator/pkg/config"
)

func getCustomScaleResourceForStatefulset(ctx context.Context, sts *appsv1.StatefulSet, restMapper meta.RESTMapper, scalesGetter scaleclient.ScalesGetter) (*autoscalingv1.Scale, string, error) {
	annotations := sts.GetAnnotations()
	name := annotations[config.RolloutMirrorReplicasFromResourceNameAnnotationKey]
	kind := annotations[config.RolloutMirrorReplicasFromResourceKindAnnotationKey]
	if name == "" || kind == "" {
		return nil, "", nil
	}

	apiVersion := annotations[config.RolloutMirrorReplicasFromResourceAPIVersionAnnotationKey]

	reference := fmt.Sprintf("%s/%s/%s", kind, sts.Namespace, name)

	targetGV, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return nil, "", fmt.Errorf("invalid API version in %s annotation: %v", config.RolloutMirrorReplicasFromResourceAPIVersionAnnotationKey, err)
	}

	targetGK := schema.GroupKind{
		Group: targetGV.Group,
		Kind:  kind,
	}

	mappings, err := restMapper.RESTMappings(targetGK)
	if err != nil {
		return nil, "", fmt.Errorf("unable to determine resource for scale target reference: %v", err)
	}

	scale, _ /*targetGR */, err := scaleForResourceMappings(ctx, sts.Namespace, name, mappings, scalesGetter)
	if err != nil {
		return nil, "", fmt.Errorf("failed to query scale subresource for %s: %v", reference, err)
	}

	return scale, fmt.Sprintf("%s/%s", kind, name), nil
}

// copied from https://github.com/kubernetes/kubernetes/blob/3c4512c6ccca066d590a33b6333198b5ed813da2/pkg/controller/podautoscaler/horizontal.go#L1336-L1358
func scaleForResourceMappings(ctx context.Context, namespace, name string, mappings []*meta.RESTMapping, scalesGetter scaleclient.ScalesGetter) (*autoscalingv1.Scale, schema.GroupResource, error) {
	var firstErr error
	for i, mapping := range mappings {
		targetGR := mapping.Resource.GroupResource()
		scale, err := scalesGetter.Scales(namespace).Get(ctx, targetGR, name, metav1.GetOptions{})
		if err == nil {
			return scale, targetGR, nil
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

	return nil, schema.GroupResource{}, firstErr
}
