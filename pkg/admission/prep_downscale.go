package admission

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

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
	client := &http.Client{
		Timeout: 5 * time.Second,
	}
	return prepDownscale(ctx, logger, ar, api, client)
}

type httpClient interface {
	Post(url, contentType string, body io.Reader) (resp *http.Response, err error)
}

func prepDownscale(ctx context.Context, logger log.Logger, ar v1.AdmissionReview, api kubernetes.Interface, client httpClient) *v1.AdmissionResponse {
	logger = log.With(logger, "name", ar.Request.Name, "resource", ar.Request.Resource.Resource, "namespace", ar.Request.Namespace)

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

	if lbls[PrepDownscaleLabelKey] != PrepDownscaleLabelValue {
		// Not labeled, nothing to do.
		return &v1.AdmissionResponse{Allowed: true}
	}

	port := lbls[PrepDownscalePortKey]
	if port == "" {
		reviewResponse := v1.AdmissionResponse{
			Allowed: false,
			Result: &metav1.Status{
				Message: fmt.Sprintf("downscale of %s/%s in %s from %d to %d replicas is not allowed because the %v label is not set or empty.", ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldReplicas, *newReplicas, PrepDownscalePortKey),
			},
		}
		level.Warn(logger).Log("msg", fmt.Sprintf("downscale not allowed because the %v label is not set or empty", PrepDownscalePortKey))
		return &reviewResponse
	}

	path := lbls[PrepDownscalePathKey]
	if path == "" {
		reviewResponse := v1.AdmissionResponse{
			Allowed: false,
			Result: &metav1.Status{
				Message: fmt.Sprintf("downscale of %s/%s in %s from %d to %d replicas is not allowed because the %v label is not set or empty.", ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldReplicas, *newReplicas, PrepDownscalePathKey),
			},
		}
		level.Warn(logger).Log("msg", fmt.Sprintf("downscale not allowed because the %v label is not set or empty", PrepDownscalePathKey))
		return &reviewResponse
	}

	// Since it's a downscale, check if the resource has the label that indicates it needs to be prepared to be downscaled.
	// Create a slice of endpoint addresses for pods to send HTTP post requests to and to fail if any don't return 200
	if !*ar.Request.DryRun {
		diff := (*oldReplicas - *newReplicas)
		type endpoint struct {
			url   string
			index int
		}
		eps := make([]endpoint, diff)

		// The DNS entry for a pod of a stateful set is
		// ingester-zone-a-0.$(servicename).$(namespace).svc.cluster.local
		// The service in this case is ingester-zone-a as well.
		// https://kubernetes.io/docs/concepts/workloads/controllers/statefulset/#stable-network-id
		for i := 0; i < int(diff); i++ {
			index := int(*oldReplicas) - i - 1 // nr in statefulset
			eps[i].url = fmt.Sprintf("%v-%v.%v.%v.svc.cluster.local:%s/ingester/%s",
				ar.Request.Name, // pod name
				index,
				ar.Request.Name, // svc name
				ar.Request.Namespace,
				port,
				path,
			)
			eps[i].index = index
		}

		g, _ := errgroup.WithContext(ctx)
		for _, ep := range eps {
			ep := ep // https://golang.org/doc/faq#closures_and_goroutines
			g.Go(func() error {
				logger = log.With(logger, "url", ep.url, "index", ep.index)
				err = addPreparedForDownscaleAnnotationToPod(ctx, api, ar.Request.Namespace, ar.Request.Name, ep.index)
				if err != nil {
					level.Debug(logger).Log("msg", "adding annotation to pod failed", "err", err)
				}
				level.Debug(logger).Log("msg", "annotation added to pod")

				resp, err := client.Post("http://"+ep.url, "application/json", nil)
				if err != nil {
					level.Error(logger).Log("msg", "error sending HTTP post request", "err", err)
					return err
				}
				if resp.StatusCode/100 != 2 {
					err := errors.New("HTTP post request returned non-2xx status code")
					body, readError := io.ReadAll(resp.Body)
					defer resp.Body.Close()
					level.Error(logger).Log("msg", err, "status", resp.StatusCode, "response_body", body)
					return errors.Join(err, readError)
				}
				level.Debug(logger).Log("msg", "pod prepared for shutdown")
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
