package admission

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"golang.org/x/sync/errgroup"
	v1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	PrepDownscalePathKey     = "grafana.com/prep-downscale-http-path"
	PrepDownscalePortKey     = "grafana.com/prep-downscale-http-port"
	PrepDownscaleLabelKey    = "grafana.com/prep-downscale"
	PrepDownscaleLabelValue  = "true"
	PrepDownscaleWebhookPath = "/admission/prep-downscale"
)

func PrepDownscale(ctx context.Context, logger log.Logger, ar v1.AdmissionReview, api *kubernetes.Clientset) *v1.AdmissionResponse {
	client := &http.Client{}
	return prepDownscale(ctx, logger, ar, api, client)
}

type httpClient interface {
	Post(url, contentType string, body io.Reader) (resp *http.Response, err error)
}

func prepDownscale(ctx context.Context, logger log.Logger, ar v1.AdmissionReview, api kubernetes.Interface, client httpClient) *v1.AdmissionResponse {
	logger = log.With(logger, "name", ar.Request.Name, "resource", ar.Request.Resource.Resource, "namespace", ar.Request.Namespace)
	level.Info(logger).Log("msg", "starting downScaleWebhook")
	defer func() { level.Info(logger).Log("msg", "finished downScaleWebhook") }()

	oldObj, oldGVK, err := codecs.UniversalDeserializer().Decode(ar.Request.OldObject.Raw, nil, nil)
	if err != nil {
		return allowErr(logger, "can't decode old object, allowing the change", err)
	}
	logger = log.With(logger, "request_gvk", oldGVK)

	oldReplicas, err := replicas(oldObj, oldGVK)
	if err != nil {
		return allowErr(logger, "can't get old replicas, allowing the change", err)
	}
	logger = log.With(logger, "old_replicas", int32PtrStr(oldReplicas))

	newObj, newGVK, err := codecs.UniversalDeserializer().Decode(ar.Request.Object.Raw, nil, nil)
	if err != nil {
		return allowErr(logger, "can't decode new object, allowing the change", err)
	}

	newReplicas, err := replicas(newObj, newGVK)
	if err != nil {
		return allowErr(logger, "can't get new replicas, allowing the change", err)
	}
	logger = log.With(logger, "new_replicas", int32PtrStr(newReplicas))

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

	level.Info(logger).Log("msg", "Got labels")
	for k, v := range lbls {
		level.Info(logger).Log("key", k, "value", v)
	}
	level.Info(logger).Log("msg", "Done with labels")

	// Since it's a downscale, check if the resource has the label that indicates it's ready to be prepared to be downscaled.
	// Create a slice of endpoint addresses for pods to send HTTP post requests to and to fail if any don't return 200
	if !*ar.Request.DryRun && lbls[PrepDownscaleLabelKey] == PrepDownscaleLabelValue {
		level.Info(logger).Log("msg", "downscale label set")
		diff := (*oldReplicas - *newReplicas)
		eps := make([]string, diff)

		// The DNS entry for a pod of a stateful set is
		// ingester-zone-a-0.$(servicename).$(namespace).svc.cluster.local
		// The service in this case is ingester-zone-a as well.
		// https://kubernetes.io/docs/concepts/workloads/controllers/statefulset/#stable-network-id
		for i := 0; i < int(diff); i++ {
			eps[i] = fmt.Sprintf("%v-%v.%v.%v.scv.cluster.local/%s:%s",
				ar.Request.Name,       // pod name
				int(*oldReplicas)-i-1, // nr in statefulset
				ar.Request.Name,       // svc name
				ar.Request.Namespace,
				lbls[PrepDownscalePathKey],
				lbls[PrepDownscalePortKey],
			)
		}

		g, _ := errgroup.WithContext(ctx)
		for _, ep := range eps {
			ep := ep // https://golang.org/doc/faq#closures_and_goroutines
			g.Go(func() error {
				resp, err := client.Post("http://"+ep, "application/json", nil)
				if err != nil {
					level.Error(logger).Log("msg", "error sending HTTP post request", "endpoint", ep, "err", err)
					return err
				}
				if resp.StatusCode != 200 {
					err := fmt.Errorf("HTTP post request returned non-200 status code")
					level.Error(logger).Log("msg", err, "endpoint", ep, "status", resp.StatusCode)
					return err
				}
				if resp.StatusCode == 200 {
					// TODO(jordanrushing): set a label on the pod to indicate it's been prepared for shutdown? Or do we need to do that?
					// If we did that, when the rollout operator reconcile loop runs, it would need to "de-prep" the pod and remove the label
					// OR
					// we could do that in the conditional below

					level.Debug(logger).Log("msg", "pod prepared for shutdown", "endpoint", ep)
					return nil
				}
				return nil
			})
		}
		err := g.Wait()
		if err != nil {
			// Down-scale operation is disallowed because a pod failed to prepare for shutdown and cannot be deleted
			reviewResponse := v1.AdmissionResponse{
				Allowed: false,
				Result: &metav1.Status{
					Message: fmt.Sprintf("downscale of %s/%s in %s from %d to %d replicas is not allowed because one or more pods failed to prepare for shutdown.", ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldReplicas, *newReplicas),
				},
			}
			level.Warn(logger).Log("msg", "downscale not allowed")
			return &reviewResponse
		}

		// Add an annotation showing when the downscales were finished
		err = addDownscaledAnnotation(ctx, api, ar.Request.Namespace, ar.Request.Name)
		if err != nil {
			// Down-scale operation is disallowed because the downscale annotation cannot be added
			reviewResponse := v1.AdmissionResponse{
				Allowed: false,
				Result: &metav1.Status{
					Message: fmt.Sprintf("downscale of %s/%s in %s from %d to %d replicas is not allowed because the downscale annotation could not be added.", ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldReplicas, *newReplicas),
				},
			}
			level.Warn(logger).Log("msg", "downscale annotation could not be added", "err", err)
			return &reviewResponse
		}

		// Down-scale operation is allowed because all pods successfully prepared for shutdown
		level.Info(logger).Log("msg", "downscale allowed")
		return &v1.AdmissionResponse{
			Allowed: true,
			Result: &metav1.Status{
				Message: fmt.Sprintf("downscale of %s/%s in %s from %d to %d replicas is allowed -- all pods successfully prepared for shutdown.", ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldReplicas, *newReplicas),
			},
		}
	}

	return &v1.AdmissionResponse{Allowed: true}
}
