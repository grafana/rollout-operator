package admission

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"golang.org/x/sync/errgroup"
	v1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/grafana/rollout-operator/pkg/util"
)

const (
	PrepareDownscaleWebhookPath = "/admission/prepare-downscale"
)

func PrepareDownscale(ctx context.Context, logger log.Logger, ar v1.AdmissionReview, api *kubernetes.Clientset, useZoneTracker bool, objectStorageProvider string, bucketName string, endpoint string, region string) *v1.AdmissionResponse {
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	var zt *zoneTracker
	if useZoneTracker {
		// TODO(jordanrushing): fix bucket creation semantics and wire-in config supporting multiple CSPs
		accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
		secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
		// TODO(jordanrushing): stop hardcoding insecure: true
		s3Config, _ := config.CreateS3ConfigYaml(bucketName, endpoint, region, accessKey, secretKey, true)
		bkt, _ := newS3BucketClient(s3Config, logger)

		zt = newZoneTracker(bkt, config.ZoneTrackerKey)
	}

	return prepareDownscale(ctx, logger, ar, api, client, zt)
}

type httpClient interface {
	Post(url, contentType string, body io.Reader) (resp *http.Response, err error)
}

func prepareDownscale(ctx context.Context, logger log.Logger, ar v1.AdmissionReview, api kubernetes.Interface, client httpClient, zt *zoneTracker) *v1.AdmissionResponse {
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

	rolloutGroup := lbls[config.RolloutGroupLabelKey]
	if rolloutGroup != "" {
		stsList, err := findStatefulSetsForRolloutGroup(ctx, api, ar.Request.Namespace, rolloutGroup)
		if err != nil {
			level.Warn(logger).Log("msg", "downscale not allowed due to error while finding other statefulsets", "err", err)
			return deny(
				"downscale of %s/%s in %s from %d to %d replicas is not allowed because finding other statefulsets failed.",
				ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldReplicas, *newReplicas,
			)
		}

		// If using zoneTracker instead of downscale annotations, check the zone file for recent downscaling.
		// Otherwise, check the downscale annotations.
		if zt != nil {
			if err := zt.loadZones(ctx, stsList); err != nil {
				level.Warn(logger).Log("msg", "downscale not allowed due to error while loading zones", "err", err)
				return deny(
					"downscale of %s/%s in %s from %d to %d replicas is not allowed because loading zones failed.",
					ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldReplicas, *newReplicas,
				)
			}
			// Check if the zone has been downscaled recently.
			foundSts, err := zt.findDownscalesDoneMinTimeAgo(stsList, ar.Request.Name)
			if err != nil {
				level.Warn(logger).Log("msg", "downscale not allowed due to error while parsing downscale timestamps from the zone file", "err", err)
				return deny(
					"downscale of %s/%s in %s from %d to %d replicas is not allowed because parsing parsing downscale timestamps from the zone file failed.",
					ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldReplicas, *newReplicas,
				)
			}
			if foundSts != nil {
				msg := fmt.Sprintf("downscale of %s/%s in %s from %d to %d replicas is not allowed because statefulset %v was downscaled at %v and is labelled to wait %s between zone downscales",
					ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldReplicas, *newReplicas, foundSts.name, foundSts.lastDownscaleTime, foundSts.waitTime)
				level.Warn(logger).Log("msg", msg, "err", err)
				return deny(msg)
			}
		} else {
			foundSts, err := findDownscalesDoneMinTimeAgo(stsList, ar.Request.Name)
			if err != nil {
				level.Warn(logger).Log("msg", "downscale not allowed due to error while parsing downscale annotations", "err", err)
				return deny(
					"downscale of %s/%s in %s from %d to %d replicas is not allowed because parsing downscale annotations failed.",
					ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldReplicas, *newReplicas,
				)
			}
			if foundSts != nil {
				msg := fmt.Sprintf("downscale of %s/%s in %s from %d to %d replicas is not allowed because statefulset %v was downscaled at %v and is labelled to wait %s between zone downscales",
					ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldReplicas, *newReplicas, foundSts.name, foundSts.lastDownscaleTime, foundSts.waitTime)
				level.Warn(logger).Log("msg", msg, "err", err)
				return deny(msg)
			}

			foundSts = findStatefulSetWithNonUpdatedReplicas(ctx, api, ar.Request.Namespace, stsList)
			if foundSts != nil {
				msg := fmt.Sprintf("downscale of %s/%s in %s from %d to %d replicas is not allowed because statefulset %v has %d non-updated replicas and %d non-ready replicas",
					ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldReplicas, *newReplicas, foundSts.name, foundSts.nonUpdatedReplicas, foundSts.nonReadyReplicas)
				level.Warn(logger).Log("msg", msg)
				return deny(msg)
			}
		}
	}

	if *ar.Request.DryRun {
		return &v1.AdmissionResponse{Allowed: true}
	}

	// Since it's a downscale, check if the resource has the label that indicates it needs to be prepared to be downscaled.
	// Create a slice of endpoint addresses for pods to send HTTP post requests to and to fail if any don't return 200
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
		eps[i].url = fmt.Sprintf("%v-%v.%v.%v.svc.cluster.local:%s/%s",
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

	if zt != nil {
		if err := zt.setDownscaled(ctx, ar.Request.Name); err != nil {
			level.Error(logger).Log("msg", "downscale not allowed due to error while setting downscale timestamp in the zone file", "err", err)
			return deny(
				"downscale of %s/%s in %s from %d to %d replicas is not allowed because setting downscale timestamp in the zone file failed.",
				ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldReplicas, *newReplicas,
			)
		}
	} else {
		err := addDownscaledAnnotationToStatefulSet(ctx, api, ar.Request.Namespace, ar.Request.Name)
		if err != nil {
			level.Error(logger).Log("msg", "downscale not allowed due to error while adding annotation", "err", err)
			return deny(
				"downscale of %s/%s in %s from %d to %d replicas is not allowed because adding an annotation to the statefulset failed.",
				ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldReplicas, *newReplicas,
			)
		}
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

func addDownscaledAnnotationToStatefulSet(ctx context.Context, api kubernetes.Interface, namespace, stsName string) error {
	client := api.AppsV1().StatefulSets(namespace)
	patch := fmt.Sprintf(`{"metadata":{"annotations":{"%v":"%v"}}}`, config.LastDownscaleAnnotationKey, time.Now().UTC().Format(time.RFC3339))
	_, err := client.Patch(ctx, stsName, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

type statefulSetDownscale struct {
	name               string
	waitTime           time.Duration
	lastDownscaleTime  time.Time
	nonReadyReplicas   int
	nonUpdatedReplicas int
}

// findDownscalesDoneMinTimeAgo returns an error if downscale annotations cannot be parsed.
func findDownscalesDoneMinTimeAgo(stsList *appsv1.StatefulSetList, stsName string) (*statefulSetDownscale, error) {
	for _, sts := range stsList.Items {
		if sts.Name == stsName {
			continue
		}
		lastDownscaleAnnotation, ok := sts.Annotations[config.LastDownscaleAnnotationKey]
		if !ok {
			// No last downscale label set on the statefulset, we can continue
			continue
		}

		lastDownscale, err := time.Parse(time.RFC3339, lastDownscaleAnnotation)
		if err != nil {
			return nil, fmt.Errorf("can't parse %v annotation of %s: %w", config.LastDownscaleAnnotationKey, sts.Name, err)
		}

		timeBetweenDownscaleLabel, ok := sts.Labels[config.MinTimeBetweenZonesDownscaleLabelKey]
		if !ok {
			// No time between downscale label set on the statefulset, we can continue
			continue
		}

		minTimeBetweenDownscale, err := time.ParseDuration(timeBetweenDownscaleLabel)
		if err != nil {
			return nil, fmt.Errorf("can't parse %v label of %s: %w", config.MinTimeBetweenZonesDownscaleLabelKey, sts.Name, err)
		}

		if time.Since(lastDownscale) < minTimeBetweenDownscale {
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

// findStatefulSetWithNonUpdatedReplicas returns any statefulset that has non-updated replicas, indicating that the countRunningAndReadyPods
// may be in the process of being rolled.
func findStatefulSetWithNonUpdatedReplicas(ctx context.Context, api kubernetes.Interface, namespace string, stsList *appsv1.StatefulSetList) *statefulSetDownscale {
	for _, sts := range stsList.Items {
		readyPods, err := countRunningAndReadyPods(ctx, api, namespace, &sts)
		if err != nil {
			return nil
		}
		status := sts.Status
		if int(status.Replicas) != readyPods || int(status.UpdatedReplicas) != readyPods {
			return &statefulSetDownscale{
				name:               sts.Name,
				nonReadyReplicas:   int(status.Replicas) - readyPods,
				nonUpdatedReplicas: int(status.Replicas - status.UpdatedReplicas),
			}
		}
	}
	return nil
}

// countRunningAndReadyPods counts running and ready pods for a StatefulSet.
func countRunningAndReadyPods(ctx context.Context, api kubernetes.Interface, namespace string, sts *appsv1.StatefulSet) (int, error) {
	pods, err := findPodsForStatefulSet(ctx, api, namespace, sts)
	if err != nil {
		return 0, err
	}

	result := 0
	for _, pod := range pods.Items {
		if util.IsPodRunningAndReady(&pod) {
			result++
		}
	}

	return result, nil
}

func findPodsForStatefulSet(ctx context.Context, api kubernetes.Interface, namespace string, sts *appsv1.StatefulSet) (*corev1.PodList, error) {
	podsSelector := labels.NewSelector().Add(
		util.MustNewLabelsRequirement("name", selection.Equals, []string{sts.Spec.Template.Labels["name"]}),
	)
	return api.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: podsSelector.String(),
	})
}

func findStatefulSetsForRolloutGroup(ctx context.Context, api kubernetes.Interface, namespace, rolloutGroup string) (*appsv1.StatefulSetList, error) {
	groupReq, err := labels.NewRequirement(config.RolloutGroupLabelKey, selection.Equals, []string{rolloutGroup})
	if err != nil {
		return nil, err
	}
	sel := labels.NewSelector().Add(*groupReq)
	return api.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: sel.String(),
	})
}
