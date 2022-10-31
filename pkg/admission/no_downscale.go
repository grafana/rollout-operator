package admission

import (
	"fmt"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	NoDownscaleLabelKey    = "grafana.com/no-downscale"
	NoDownscaleLabelValue  = "true"
	NoDownscaleWebhookPath = "/admission/no-downscale"
)

func NoDownscale(logger log.Logger, ar v1.AdmissionReview) *v1.AdmissionResponse {
	reviewResponse := v1.AdmissionResponse{}
	reviewResponse.Allowed = true

	oldObj, oldGVK, err := codecs.UniversalDeserializer().Decode(ar.Request.OldObject.Raw, nil, nil)
	if err != nil {
		level.Error(logger).Log("msg", "can't decode old object, allowing the change", "err", err)
		reviewResponse.Warnings = append(reviewResponse.Warnings, fmt.Sprintf("can't decode old object, allowing the change, err: %s", err))
		return &reviewResponse
	}

	oldReplicas, err := replicas(oldObj, oldGVK)
	if err != nil {
		level.Error(logger).Log("msg", "can't get old replicas, allowing the change", "err", err)
		reviewResponse.Warnings = append(reviewResponse.Warnings, fmt.Sprintf("can't get old replicas, allowing the change, err: %s", err))
		return &reviewResponse
	}

	newObj, newGVK, err := codecs.UniversalDeserializer().Decode(ar.Request.Object.Raw, nil, nil)
	if err != nil {
		level.Error(logger).Log("msg", "can't decode new object, allowing the change", "err", err)
		reviewResponse.Warnings = append(reviewResponse.Warnings, fmt.Sprintf("can't decode new object, allowing the change, err: %s", err))
		return &reviewResponse
	}

	newReplicas, err := replicas(newObj, newGVK)
	if err != nil {
		level.Error(logger).Log("msg", "can't get new replicas, allowing the change", "err", err)
		reviewResponse.Warnings = append(reviewResponse.Warnings, fmt.Sprintf("can't get new replicas, allowing the change, err: %s", err))
		return &reviewResponse
	}

	if oldReplicas == nil {
		level.Error(logger).Log("msg", "old replicas is not defined, allowing the change")
		reviewResponse.Warnings = append(reviewResponse.Warnings, "old replicas is not defined, allowing the change")
		return &reviewResponse
	}

	if newReplicas == nil {
		level.Error(logger).Log("msg", "new replicas is not defined, allowing the change")
		reviewResponse.Warnings = append(reviewResponse.Warnings, "new replicas is not defined, allowing the change")
		return &reviewResponse
	}

	if *oldReplicas > *newReplicas {
		name, err := meta.NewAccessor().Name(newObj)
		if err != nil {
			name = fmt.Sprintf("unknown (can't get name: %s)", err)
		}
		reviewResponse.Allowed = false
		reviewResponse.Result = &metav1.Status{
			Message: fmt.Sprintf("downscale of %s (%s) from %d to %d replicas is not allowed because it has the label '%s=%s'", name, newGVK.GroupKind(), *oldReplicas, *newReplicas, NoDownscaleLabelKey, NoDownscaleLabelValue),
		}
	}

	return &reviewResponse
}

func replicas(obj runtime.Object, gvk *schema.GroupVersionKind) (*int32, error) {
	switch o := obj.(type) {
	case *appsv1.Deployment:
		return o.Spec.Replicas, nil
	case *appsv1.StatefulSet:
		return o.Spec.Replicas, nil
	case *appsv1.ReplicaSet:
		return o.Spec.Replicas, nil
	default:
		return nil, fmt.Errorf("unsupported type %s (go type %T)", gvk, obj)
	}
}
