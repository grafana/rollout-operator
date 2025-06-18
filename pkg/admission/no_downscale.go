package admission

import (
	"context"
	"fmt"
	"strconv"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/spanlogger"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	v1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"

	"github.com/grafana/rollout-operator/pkg/config"
)

const (
	NoDownscaleWebhookPath = "/admission/no-downscale"
)

var tracer = otel.Tracer("pkg/admission")

func NoDownscale(ctx context.Context, l log.Logger, ar v1.AdmissionReview, api *kubernetes.Clientset) *v1.AdmissionResponse {
	logger, ctx := spanlogger.New(ctx, l, "admission.NoDownscale()", tenantResolver)
	defer logger.Finish()

	logger.SetSpanAndLogTag("object.name", ar.Request.Name)
	logger.SetSpanAndLogTag("object.resource", ar.Request.Resource.Resource)
	logger.SetSpanAndLogTag("object.namespace", ar.Request.Namespace)

	oldObj, oldGVK, err := codecs.UniversalDeserializer().Decode(ar.Request.OldObject.Raw, nil, nil)
	if err != nil {
		return allowErr(logger, "can't decode old object, allowing the change", err)
	}
	logger.SetSpanAndLogTag("request.gvk", oldGVK)

	oldReplicas, err := replicas(oldObj, oldGVK)
	if err != nil {
		return allowErr(logger, "can't get old replicas, allowing the change", err)
	}
	logger.SetSpanAndLogTag("object.old_replicas", int32PtrStr(oldReplicas))

	newObj, newGVK, err := codecs.UniversalDeserializer().Decode(ar.Request.Object.Raw, nil, nil)
	if err != nil {
		return allowErr(logger, "can't decode new object, allowing the change", err)
	}

	newReplicas, err := replicas(newObj, newGVK)
	if err != nil {
		return allowErr(logger, "can't get new replicas, allowing the change", err)
	}
	logger.SetSpanAndLogTag("object.new_replicas", int32PtrStr(newReplicas))

	// Both replicas are nil, nothing to warn about.
	if oldReplicas == nil && newReplicas == nil {
		level.Debug(logger).Log("msg", "no replicas change, allowing")
		return &v1.AdmissionResponse{Allowed: true}
	}
	// Changes from/to nil scale are not downscales strictly speaking.
	if oldReplicas == nil || newReplicas == nil {
		return allowWarn(logger, "old/new replicas is nil, allowing the change")
	}
	// If it's not a downscale, just log debug.
	if *oldReplicas < *newReplicas {
		level.Debug(logger).Log("msg", "upscale allowed")
		return &v1.AdmissionResponse{Allowed: true}
	}
	if *oldReplicas == *newReplicas {
		level.Debug(logger).Log("msg", "no replicas change, allowing")
		return &v1.AdmissionResponse{Allowed: true}
	}

	// Get the resource labels: for example, for a StatefulSet, it will be the labels of the StatefulSet itself,
	// while for a Scale object, it will be the labels of the StatefulSet/Deployment/ReplicaSet that the Scale object belongs to.
	var lbls map[string]string
	switch o := oldObj.(type) {
	case *appsv1.Deployment:
		lbls = o.Labels
	case *appsv1.StatefulSet:
		lbls = o.Labels
	case *appsv1.ReplicaSet:
		lbls = o.Labels
	case *autoscalingv1.Scale:
		lbls, err = getResourceLabels(ctx, ar, api)
		if err != nil {
			return allowBecauseCannotGetResource(ar, logger, err)
		}
	default:
		return allowWarn(logger, fmt.Sprintf("unsupported type %T, allowing the change", o))
	}

	// Check resource label.
	if val, ok := lbls[config.NoDownscaleLabelKey]; !ok {
		level.Info(logger).Log("msg", fmt.Sprintf("downscale allowed because resource does not have the label %q", config.NoDownscaleLabelKey))
		return &v1.AdmissionResponse{Allowed: true}
	} else if val != config.NoDownscaleLabelValue {
		level.Info(logger).Log("msg", fmt.Sprintf("downscale allowed because resouce's label %q value is not %q", config.NoDownscaleLabelKey, config.NoDownscaleLabelValue), "label_value", val)
		return &v1.AdmissionResponse{Allowed: true}
	}

	// Has the label, disallow the change.
	reviewResponse := v1.AdmissionResponse{
		Allowed: false,
		Result: &metav1.Status{
			Message: fmt.Sprintf("downscale of %s/%s in %s from %d to %d replicas is not allowed because it has the label '%s=%s'", ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldReplicas, *newReplicas, config.NoDownscaleLabelKey, config.NoDownscaleLabelValue),
		},
	}
	level.Warn(logger).Log("msg", "downscale not allowed")

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
	case *autoscalingv1.Scale:
		return &o.Spec.Replicas, nil
	default:
		return nil, fmt.Errorf("unsupported type %s (go type %T)", gvk, obj)
	}
}

func allowWarn(logger log.Logger, warn string) *v1.AdmissionResponse {
	reviewResponse := v1.AdmissionResponse{}
	reviewResponse.Allowed = true
	level.Warn(logger).Log("msg", warn)
	reviewResponse.Warnings = append(reviewResponse.Warnings, warn)
	return &reviewResponse

}

func allowErr(logger log.Logger, msg string, err error) *v1.AdmissionResponse {
	reviewResponse := v1.AdmissionResponse{}
	reviewResponse.Allowed = true
	level.Error(logger).Log("msg", msg, "err", err)
	reviewResponse.Warnings = append(reviewResponse.Warnings, fmt.Sprintf("%s, err: %s", msg, err))
	return &reviewResponse
}

func getResourceLabels(ctx context.Context, ar v1.AdmissionReview, api kubernetes.Interface) (map[string]string, error) {
	ctx, span := tracer.Start(ctx, "admission.getResourceLabels()", trace.WithAttributes(
		attribute.String("object.namespace", ar.Request.Namespace),
		attribute.String("object.name", ar.Request.Name),
		attribute.String("object.resource", ar.Request.Resource.Resource),
	))
	defer span.End()

	switch ar.Request.Resource.Resource {
	case "statefulsets":
		obj, err := api.AppsV1().StatefulSets(ar.Request.Namespace).Get(ctx, ar.Request.Name, metav1.GetOptions{})
		return obj.Labels, err
	case "deployments":
		obj, err := api.AppsV1().Deployments(ar.Request.Namespace).Get(ctx, ar.Request.Name, metav1.GetOptions{})
		return obj.Labels, err
	case "replicasets":
		obj, err := api.AppsV1().ReplicaSets(ar.Request.Namespace).Get(ctx, ar.Request.Name, metav1.GetOptions{})
		return obj.Labels, err
	}
	return nil, fmt.Errorf("unsupported resource %s", ar.Request.Resource.Resource)
}

func allowBecauseCannotGetResource(ar v1.AdmissionReview, logger log.Logger, err error) *v1.AdmissionResponse {
	reviewResponse := v1.AdmissionResponse{}
	reviewResponse.Allowed = true
	msg := fmt.Sprintf("can't get %s/%s in namespace %s, allowing the change", ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace)
	level.Error(logger).Log("msg", msg, "err", err)
	reviewResponse.Warnings = append(reviewResponse.Warnings, fmt.Sprintf("%s: %s", msg, err))
	return &reviewResponse
}

func int32PtrStr(i *int32) string {
	if i == nil {
		return "<nil>"
	}
	return strconv.Itoa(int(*i))
}
