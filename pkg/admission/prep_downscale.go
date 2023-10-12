package admission

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"golang.org/x/sync/errgroup"
	v1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/kubernetes"

	"github.com/grafana/rollout-operator/pkg/config"
)

const (
	PrepareDownscaleWebhookPath = "/admission/prepare-downscale"
	extraPods                   = 3
)

func PrepareDownscale(ctx context.Context, logger log.Logger, ar v1.AdmissionReview, api *kubernetes.Clientset) *v1.AdmissionResponse {
	client := &http.Client{
		Timeout: 1 * time.Second,
	}
	return prepareDownscale(ctx, logger, ar, api, client)
}

type httpClient interface {
	Post(url, contentType string, body io.Reader) (resp *http.Response, err error)
	Get(url string) (resp *http.Response, err error)
}

func prepareDownscale(ctx context.Context, logger log.Logger, ar v1.AdmissionReview, api kubernetes.Interface, client httpClient) *v1.AdmissionResponse {
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

	var annotations map[string]string
	switch o := oldObj.(type) {
	case *appsv1.Deployment:
		annotations = o.Annotations
	case *appsv1.StatefulSet:
		annotations = o.Annotations
	case *appsv1.ReplicaSet:
		annotations = o.Annotations
	case *autoscalingv1.Scale:
		annotations, err = getResourceAnnotations(ctx, ar, api)
		if err != nil {
			return allowBecauseCannotGetResource(ar, logger, err)
		}
	default:
		return allowWarn(logger, fmt.Sprintf("unsupported type %T, allowing the change", o))
	}

	// Since it's a downscale, check if the resource has the label that indicates it needs to be prepared to be downscaled.
	if lbls[config.PrepareDownscaleLabelKey] != config.PrepareDownscaleLabelValue {
		// Not labeled, nothing to do.
		return &v1.AdmissionResponse{Allowed: true}
	}

	port := annotations[config.PrepareDownscalePortAnnotationKey]
	if port == "" {
		level.Warn(logger).Log("msg", fmt.Sprintf("downscale not allowed because the %v annotation is not set or empty", config.PrepareDownscalePortAnnotationKey))
		return deny(
			"downscale of %s/%s in %s from %d to %d replicas is not allowed because the %v annotation is not set or empty.",
			ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldReplicas, *newReplicas, config.PrepareDownscalePortAnnotationKey,
		)
	}

	path := annotations[config.PrepareDownscalePathAnnotationKey]
	if path == "" {
		level.Warn(logger).Log("msg", fmt.Sprintf("downscale not allowed because the %v annotation is not set or empty", config.PrepareDownscalePathAnnotationKey))
		return deny(
			"downscale of %s/%s in %s from %d to %d replicas is not allowed because the %v annotation is not set or empty.",
			ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldReplicas, *newReplicas, config.PrepareDownscalePathAnnotationKey,
		)
	}

	diff := (*oldReplicas - *newReplicas)
	rolloutGroup := lbls[config.RolloutGroupLabelKey]
	level.Debug(logger).Log("msg", "checking rollout group", "group", rolloutGroup)

	if rolloutGroup != "" {
		foundSts, err := findDownscalesDoneMinTimeAgo(ctx, logger, api, client, ar, rolloutGroup, port, path, diff, *oldReplicas)
		if err != nil {
			level.Warn(logger).Log("msg", "downscale not allowed due to error while parsing downscale annotations", "err", err)
			return deny(
				"downscale of %s/%s in %s from %d to %d replicas is not allowed because parsing downscale annotations failed.",
				ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldReplicas, *newReplicas,
			)
		}
		if foundSts != nil {
			level.Warn(logger).Log("msg", "downscale not allowed because another statefulset was downscaled recently")
			return deny(
				"downscale of %s/%s in %s from %d to %d replicas is not allowed because statefulset %v was downscaled at %v and is labelled to wait %s between zone downscales",
				ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldReplicas, *newReplicas, foundSts.name, foundSts.lastDownscaleTime, foundSts.waitTime,
			)
		}
	}

	if *ar.Request.DryRun {
		return &v1.AdmissionResponse{Allowed: true}
	}

	// Create a slice of endpoint addresses for pods to send HTTP post requests to and to fail if any don't return 200
	// Also send the post response with ?unset=true to extraPods more pods to store the last timestamp
	eps := changedEndpoints(*oldReplicas, diff, extraPods, ar, ar.Request.Name, port, path)

	g, _ := errgroup.WithContext(ctx)
	for _, ep := range eps {
		ep := ep // https://golang.org/doc/faq#closures_and_goroutines
		g.Go(func() error {
			logger := log.With(logger, "url", ep.url, "index", ep.index)

			resp, err := client.Post("http://"+ep.url, "application/json", nil)
			if err != nil {
				level.Error(logger).Log("msg", "error sending HTTP post request", "err", err)
				return err
			}
			if resp.StatusCode/100 != 2 {
				err := errors.New("HTTP post request returned non-2xx status code")
				body, readError := io.ReadAll(resp.Body)
				defer resp.Body.Close()
				level.Error(logger).Log("msg", "error received from shutdown endpoint", "err", err, "status", resp.StatusCode, "response_body", body)
				return errors.Join(err, readError)
			}
			level.Debug(logger).Log("msg", "pod prepared for shutdown")
			return nil
		})
	}
	err = g.Wait()
	if err != nil {
		// Down-scale operation is disallowed because a pod failed to prepare for shutdown and cannot be deleted
		level.Error(logger).Log("msg", "downscale not allowed due to error", "err", err)
		return deny(
			"downscale of %s/%s in %s from %d to %d replicas is not allowed because one or more pods failed to prepare for shutdown.",
			ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldReplicas, *newReplicas,
		)
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

// deny returns a *v1.AdmissionResponse with Allowed: false and the message provided formatted with as in fmt.Sprintf.
func deny(msg string, args ...any) *v1.AdmissionResponse {
	return &v1.AdmissionResponse{
		Allowed: false,
		Result: &metav1.Status{
			Message: fmt.Sprintf(msg, args...),
		},
	}
}

func getResourceAnnotations(ctx context.Context, ar v1.AdmissionReview, api kubernetes.Interface) (map[string]string, error) {
	switch ar.Request.Resource.Resource {
	case "statefulsets":
		obj, err := api.AppsV1().StatefulSets(ar.Request.Namespace).Get(ctx, ar.Request.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return obj.Annotations, nil
	}
	return nil, fmt.Errorf("unsupported resource %s", ar.Request.Resource.Resource)
}

type statefulSetDownscale struct {
	name              string
	waitTime          time.Duration
	lastDownscaleTime time.Time
}

func findDownscalesDoneMinTimeAgo(ctx context.Context, logger log.Logger, api kubernetes.Interface, httpClient httpClient, ar v1.AdmissionReview, rolloutGroup, port, path string, diff, oldReplicas int32) (*statefulSetDownscale, error) {
	apiClient := api.AppsV1().StatefulSets(ar.Request.Namespace)
	groupReq, err := labels.NewRequirement(config.RolloutGroupLabelKey, selection.Equals, []string{rolloutGroup})
	if err != nil {
		return nil, err
	}
	sel := labels.NewSelector().Add(*groupReq)
	list, err := apiClient.List(ctx, metav1.ListOptions{
		LabelSelector: sel.String(),
	})
	if err != nil {
		return nil, err
	}

	for _, sts := range list.Items {
		if sts.Name == ar.Request.Name {
			continue
		}
		timeBetweenDownscaleLabel, ok := sts.Labels[config.MinTimeBetweenZonesDownscaleLabelKey]
		if !ok {
			// No time between downscale label set on the statefulset, we can continue
			level.Debug(logger).Log("msg", "no min time between zones label, continue")
			continue
		}

		minTimeBetweenDownscale, err := time.ParseDuration(timeBetweenDownscaleLabel)
		if err != nil {
			level.Debug(logger).Log("msg", fmt.Sprintf("can't parse %v label of %s", config.MinTimeBetweenZonesDownscaleLabelKey, sts.Name), "err", err)
			return nil, fmt.Errorf("can't parse %v label of %s: %w", config.MinTimeBetweenZonesDownscaleLabelKey, sts.Name, err)
		}

		replicas := *sts.Spec.Replicas

		wg := sync.WaitGroup{}
		eps := changedEndpoints(replicas, diff, extraPods, ar, sts.Name, port, path)
		timestamps := make(chan time.Time, len(eps))
		for _, ep := range eps {
			wg.Add(1)
			ep := ep
			go func(ep endpoint) {
				defer wg.Done()
				// Get time of last prepare_shutdown from an ingester
				// These calls can fail if the ingester is down
				logger := log.With(logger, "url", ep.url)
				resp, err := httpClient.Get("http://" + ep.url)
				if err != nil {
					level.Debug(logger).Log("msg", "error sending HTTP get request", "err", err)
					return
				}
				defer resp.Body.Close()
				if resp.StatusCode/100 != 2 {
					err := errors.New("HTTP get request returned non-2xx status code")
					body, readError := io.ReadAll(resp.Body)
					defer resp.Body.Close()
					level.Debug(logger).Log("msg", "error received from shutdown endpoint", "err", errors.Join(err, readError), "status", resp.StatusCode, "response_body", body)
					return
				}

				bts, err := io.ReadAll(resp.Body)
				if err != nil {
					level.Debug(logger).Log("msg", "error reading body of HTTP get request", "err", err)
					return
				}

				// The ingester will return either `unset`,  `unset <timestamp>` or `set <timestamp>`
				splits := strings.SplitN(string(bts), " ", 2)
				if len(splits) != 2 {
					// If `unset` only no timestamp was ever set so the downscale can go ahead
					return
				}

				lastDownscale, err := time.Parse(time.RFC3339, splits[1])
				if err != nil {
					level.Error(logger).Log("msg", "error parsing timestamp", "err", err, "timestamp", splits[1])
					return
				}

				timestamps <- lastDownscale
			}(ep)
		}
		wg.Wait()
		close(timestamps)

		var lastDownscale time.Time
		for t := range timestamps {
			if t.After(lastDownscale) {
				lastDownscale = t
			}
		}

		// No timestamp found: this can happen in the beginning or when downscaling to zero
		if lastDownscale.IsZero() {
			level.Debug(logger).Log("msg", "last downscale time is zero", "time", lastDownscale.String())
			continue
		}

		level.Debug(logger).Log("msg", "checking time since last downscale", "minTimeBetween", minTimeBetweenDownscale.String(), "timeSince", time.Since(lastDownscale).String())
		if time.Since(lastDownscale) <= minTimeBetweenDownscale {
			s := statefulSetDownscale{
				name:              sts.Name,
				waitTime:          minTimeBetweenDownscale,
				lastDownscaleTime: lastDownscale,
			}
			return &s, nil
		}
	}
	return nil, nil
}

type endpoint struct {
	url   string
	index int
}

func changedEndpoints(oldReplicas, diffShutdown, extra int32, ar v1.AdmissionReview, stsName, port, path string) []endpoint {
	var eps []endpoint

	// The DNS entry for a pod of a stateful set is
	// ingester-zone-a-0.$(servicename).$(namespace).svc.cluster.local
	// The service in this case is ingester-zone-a as well.
	// https://kubernetes.io/docs/concepts/workloads/controllers/statefulset/#stable-network-id
	for i := 0; i < int(diffShutdown); i++ {
		index := int(oldReplicas) - i - 1 // nr in statefulset
		if index < 0 {
			continue
		}
		var ep endpoint
		ep.url = fmt.Sprintf("%v-%v.%v.%v.svc.cluster.local:%s/%s",
			stsName, // pod name
			index,
			stsName, // svc name
			ar.Request.Namespace,
			port,
			path,
		)
		ep.index = index
		eps = append(eps, ep)
	}

	// These will not be prepared for shutdown but will contain the current time
	for i := 0; i < int(extra); i++ {
		index := int(oldReplicas) - int(diffShutdown) - i - 1
		if index < 0 {
			continue
		}
		var ep endpoint
		ep.url = fmt.Sprintf("%v-%v.%v.%v.svc.cluster.local:%s/%s?unset=true",
			stsName, // pod name
			index,
			stsName, // svc name
			ar.Request.Namespace,
			port,
			path,
		)
		ep.index = index
		eps = append(eps, ep)
	}

	return eps
}
