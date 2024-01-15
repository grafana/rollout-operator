package admission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/spanlogger"
	"github.com/opentracing-contrib/go-stdlib/nethttp"
	"github.com/opentracing/opentracing-go"
	"github.com/prometheus/common/model"
	"golang.org/x/sync/errgroup"
	v1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/grafana/rollout-operator/pkg/util"
)

const (
	PrepareDownscaleWebhookPath = "/admission/prepare-downscale"
)

func PrepareDownscale(ctx context.Context, logger log.Logger, ar v1.AdmissionReview, api *kubernetes.Clientset, useZoneTracker bool, zoneTrackerConfigMapName string) *v1.AdmissionResponse {
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &nethttp.Transport{RoundTripper: http.DefaultTransport},
	}

	if useZoneTracker {
		zt := newZoneTracker(api, ar.Request.Namespace, zoneTrackerConfigMapName)
		return zt.prepareDownscale(ctx, logger, ar, api, client)
	}

	return prepareDownscale(ctx, logger, ar, api, client)
}

type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

func prepareDownscale(ctx context.Context, l log.Logger, ar v1.AdmissionReview, api kubernetes.Interface, client httpClient) *v1.AdmissionResponse {
	logger, ctx := spanlogger.New(ctx, l, "admission.prepareDownscale()", tenantResolver)
	defer logger.Span.Finish()

	logger.SetSpanAndLogTag("object.name", ar.Request.Name)
	logger.SetSpanAndLogTag("object.resource", ar.Request.Resource.Resource)
	logger.SetSpanAndLogTag("object.namespace", ar.Request.Namespace)
	logger.SetSpanAndLogTag("request.dry_run", *ar.Request.DryRun)

	if *ar.Request.DryRun {
		return &v1.AdmissionResponse{Allowed: true}
	}

	oldInfo, err := decodeAndReplicas(ar.Request.OldObject.Raw)
	if err != nil {
		return allowErr(logger, "can't decode old object, allowing the change", err)
	}
	logger.SetSpanAndLogTag("request.gvk", oldInfo.gvk)
	logger.SetSpanAndLogTag("object.old_replicas", int32PtrStr(oldInfo.replicas))

	newInfo, err := decodeAndReplicas(ar.Request.Object.Raw)
	if err != nil {
		return allowErr(logger, "can't decode new object, allowing the change", err)
	}
	logger.SetSpanAndLogTag("object.new_replicas", int32PtrStr(newInfo.replicas))

	// Continue if it's a downscale
	_, response := checkReplicasChange(logger, oldInfo, newInfo)
	if response != nil {
		// TODO: if newReplicas (first returned value from checkReplicasChange) >= 0, and some pods in replica set (up to newReplicas) have "prepared-for-downscale" annotation set, we will call DELETE on prepare-for-downscale URL.
		return response
	}

	// Get the labels and annotations from the old object including the prepare downscale label
	lbls, annotations, err := getLabelsAndAnnotations(ctx, ar, api, oldInfo)
	if err != nil {
		return allowWarn(logger, fmt.Sprintf("%s, allowing the change", err))
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
			ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas, config.PrepareDownscalePortAnnotationKey,
		)
	}

	path := annotations[config.PrepareDownscalePathAnnotationKey]
	if path == "" {
		level.Warn(logger).Log("msg", fmt.Sprintf("downscale not allowed because the %v annotation is not set or empty", config.PrepareDownscalePathAnnotationKey))
		return deny(
			"downscale of %s/%s in %s from %d to %d replicas is not allowed because the %v annotation is not set or empty.",
			ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas, config.PrepareDownscalePathAnnotationKey,
		)
	}

	prepareDownscaleMinDelayBeforeShutdown := time.Duration(0)
	{
		delayStr := annotations[config.PrepareDownscaleMinDelayBeforeShutdown]
		if delayStr != "" {
			parsedDelay, err := model.ParseDuration(delayStr)
			if err != nil {
				level.Warn(logger).Log("msg", fmt.Sprintf("ignoring misconfigured %v annotation that cannot be parsed as duration", config.PrepareDownscaleMinDelayBeforeShutdown), "err", err)
				// TODO: shall we abort downscale in this case?
			} else {
				prepareDownscaleMinDelayBeforeShutdown = time.Duration(parsedDelay)
			}
		}
	}

	rolloutGroup := lbls[config.RolloutGroupLabelKey]
	if rolloutGroup != "" {
		stsList, err := findStatefulSetsForRolloutGroup(ctx, api, ar.Request.Namespace, rolloutGroup)
		if err != nil {
			level.Warn(logger).Log("msg", "downscale not allowed due to error while finding other statefulsets", "err", err)
			return deny(
				"downscale of %s/%s in %s from %d to %d replicas is not allowed because finding other statefulsets failed.",
				ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas,
			)
		}
		foundSts, err := findDownscalesDoneMinTimeAgo(stsList, ar.Request.Name)
		if err != nil {
			level.Warn(logger).Log("msg", "downscale not allowed due to error while parsing downscale annotations", "err", err)
			return deny(
				"downscale of %s/%s in %s from %d to %d replicas is not allowed because parsing downscale annotations failed.",
				ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas,
			)
		}
		if foundSts != nil {
			msg := fmt.Sprintf("downscale of %s/%s in %s from %d to %d replicas is not allowed because statefulset %v was downscaled at %v and is labelled to wait %s between zone downscales",
				ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas, foundSts.name, foundSts.lastDownscaleTime, foundSts.waitTime)
			level.Warn(logger).Log("msg", msg, "err", err)
			return deny(msg)
		}
		foundSts, err = findStatefulSetWithNonUpdatedReplicas(ctx, api, ar.Request.Namespace, stsList, ar.Request.Name)
		if err != nil {
			msg := fmt.Sprintf("downscale of %s/%s in %s from %d to %d replicas is not allowed because an error occurred while checking whether StatefulSets have non-updated replicas",
				ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas)
			level.Warn(logger).Log("msg", msg, "err", err)
			return deny(msg)
		}
		if foundSts != nil {
			msg := fmt.Sprintf("downscale of %s/%s in %s from %d to %d replicas is not allowed because statefulset %v has %d non-updated replicas and %d non-ready replicas",
				ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas, foundSts.name, foundSts.nonUpdatedReplicas, foundSts.nonReadyReplicas)
			level.Warn(logger).Log("msg", msg)
			return deny(msg)
		}
	}

	// Since it's a downscale, check if the resource has the label that indicates it needs to be prepared to be downscaled.
	// Create a slice of endpoint addresses for pods to send HTTP POST requests to and to fail if any don't return 200
	eps := createEndpoints(ar, oldInfo, newInfo, port, path)

	useAndUpdateLastPrepareDownscaleTimestamp := prepareDownscaleMinDelayBeforeShutdown > 0
	if err := sendPrepareShutdownRequests(ctx, logger, client, eps, useAndUpdateLastPrepareDownscaleTimestamp, api); err != nil {
		// Down-scale operation is disallowed because a pod failed to prepare for shutdown and cannot be deleted
		level.Error(logger).Log("msg", "downscale not allowed due to error", "err", err)
		return deny(
			"downscale of %s/%s in %s from %d to %d replicas is not allowed because one or more pods failed to prepare for shutdown.",
			ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas,
		)
	}

	if prepareDownscaleMinDelayBeforeShutdown > 0 {
		// Check last prepare-downscale timestamp for all affected pods, and compute maximum. If some pods don't have annotation set,
		// or delay since the maximum hasn't been reached yet, we deny the downscale.
		resp := checkPrepareDownscaleMinDelayBeforeShutdown(ctx, logger, eps, api, prepareDownscaleMinDelayBeforeShutdown, ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace)
		if resp != nil {
			return response
		}
	}

	if err := addDownscaledAnnotationToStatefulSet(ctx, api, ar.Request.Namespace, ar.Request.Name); err != nil {
		level.Error(logger).Log("msg", "downscale not allowed due to error while adding annotation", "err", err)
		return deny(
			"downscale of %s/%s in %s from %d to %d replicas is not allowed because adding an annotation to the statefulset failed.",
			ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas,
		)
	}

	// Down-scale operation is allowed because all pods successfully prepared for shutdown
	level.Info(logger).Log("msg", "downscale allowed")
	return &v1.AdmissionResponse{
		Allowed: true,
		Result: &metav1.Status{
			Message: fmt.Sprintf("downscale of %s/%s in %s from %d to %d replicas is allowed -- all pods successfully prepared for shutdown.", ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas),
		},
	}
}

func checkPrepareDownscaleMinDelayBeforeShutdown(ctx context.Context, logger log.Logger, eps []endpoint, api kubernetes.Interface, minDelay time.Duration, resourceType, resourceName, namespace string) interface{} {
	if len(eps) == 0 {
		return nil
	}

	span, ctx := opentracing.StartSpanFromContext(ctx, "admission.checkPrepareDownscaleMinDelayBeforeShutdown()")
	defer span.Finish()

	var maxTime time.Time
	for _, ep := range eps {
		t, err := getLastPrepareDownscaleAnnotationOnPod(ctx, api, ep.namespace, ep.podName)
		if err != nil {
			level.Error(logger).Log("msg", fmt.Sprintf("failed to check %s annotation on pod", config.LastPrepareDownscaleAnnotationKey), "pod", ep.podName, "err", err)
			return deny(fmt.Sprintf("downscale of %s/%s in %s not allowed, as %s cannot be verified for pod %s", resourceType, resourceName, namespace, config.PrepareDownscaleMinDelayBeforeShutdown, ep.podName))
		}

		if t.IsZero() {
			level.Error(logger).Log("msg", fmt.Sprintf("%s annotation on pod is not set", config.LastPrepareDownscaleAnnotationKey), "pod", ep.podName)
			return deny(fmt.Sprintf("downscale of %s/%s in %s not allowed, as %s has not been set for pod %s", resourceType, resourceName, namespace, config.LastPrepareDownscaleAnnotationKey, ep.podName))
		}

		if t.After(maxTime) {
			maxTime = t
		}
	}

	elapsedSinceMaxTime := time.Since(maxTime)
	if elapsedSinceMaxTime < minDelay {
		level.Warn(logger).Log("msg", fmt.Sprintf("%s has not been reached for all pods yet, rejecting downscale", config.PrepareDownscaleMinDelayBeforeShutdown), "minDelay", minDelay, "elapsed", elapsedSinceMaxTime)
		return deny(fmt.Sprintf("downscale of %s/%s in %s not allowed, as %s has not been reached for all pods. elapsed time: %v", resourceType, resourceName, namespace, config.MinTimeBetweenZonesDownscaleLabelKey, elapsedSinceMaxTime))
	}

	level.Info(logger).Log("msg", fmt.Sprintf("%s has been reached for all pods, allowing downscale", config.PrepareDownscaleMinDelayBeforeShutdown), "minDelay", minDelay, "elapsed", elapsedSinceMaxTime)
	return nil
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
	span, ctx := opentracing.StartSpanFromContext(ctx, "admission.getResourceAnnotations()")
	defer span.Finish()

	span.SetTag("object.namespace", ar.Request.Namespace)
	span.SetTag("object.name", ar.Request.Name)

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
	span, ctx := opentracing.StartSpanFromContext(ctx, "admission.addDownscaledAnnotationToStatefulSet()")
	defer span.Finish()

	span.SetTag("object.namespace", namespace)
	span.SetTag("object.name", stsName)

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

// findDownscalesDoneMinTimeAgo checks whether there's any StatefulSet in the stsList which has been downscaled
// less than "min allowed time" ago. The timestamp of the last downscale and the minimum time required between
// downscales are set as StatefulSet annotation and label respectively. If such annotations and labels can't be
// parsed, then this function returns an error.
//
// The StatefulSet whose name matches the input excludeStsName is not checked.
func findDownscalesDoneMinTimeAgo(stsList *appsv1.StatefulSetList, excludeStsName string) (*statefulSetDownscale, error) {
	for _, sts := range stsList.Items {
		if sts.Name == excludeStsName {
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
//
// The StatefulSet whose name matches the input excludeStsName is not checked.
func findStatefulSetWithNonUpdatedReplicas(ctx context.Context, api kubernetes.Interface, namespace string, stsList *appsv1.StatefulSetList, excludeStsName string) (*statefulSetDownscale, error) {
	span, ctx := opentracing.StartSpanFromContext(ctx, "admission.findStatefulSetWithNonUpdatedReplicas()")
	defer span.Finish()

	span.SetTag("object.namespace", namespace)

	for _, sts := range stsList.Items {
		if sts.Name == excludeStsName {
			continue
		}
		readyPods, err := countRunningAndReadyPods(ctx, api, namespace, &sts)
		if err != nil {
			return nil, err
		}
		status := sts.Status
		if int(status.Replicas) != readyPods || int(status.UpdatedReplicas) != readyPods {
			return &statefulSetDownscale{
				name:               sts.Name,
				nonReadyReplicas:   int(status.Replicas) - readyPods,
				nonUpdatedReplicas: int(status.Replicas - status.UpdatedReplicas),
			}, nil
		}
	}
	return nil, nil
}

// countRunningAndReadyPods counts running and ready pods for a StatefulSet.
func countRunningAndReadyPods(ctx context.Context, api kubernetes.Interface, namespace string, sts *appsv1.StatefulSet) (int, error) {
	span, ctx := opentracing.StartSpanFromContext(ctx, "admission.countRunningAndReadyPods()")
	defer span.Finish()

	span.SetTag("object.namespace", namespace)
	span.SetTag("object.name", sts.Name)

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
	span, ctx := opentracing.StartSpanFromContext(ctx, "admission.findStatefulSetsForRolloutGroup()")
	defer span.Finish()

	span.SetTag("object.namespace", namespace)
	span.SetTag("rollout_group", rolloutGroup)

	groupReq, err := labels.NewRequirement(config.RolloutGroupLabelKey, selection.Equals, []string{rolloutGroup})
	if err != nil {
		return nil, err
	}
	sel := labels.NewSelector().Add(*groupReq)
	return api.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: sel.String(),
	})
}

type objectInfo struct {
	obj      runtime.Object
	gvk      *schema.GroupVersionKind
	replicas *int32
}

type endpoint struct {
	namespace string
	podName   string
	url       string
	index     int
}

// Decode the raw object and get the number of replicas
func decodeAndReplicas(raw []byte) (*objectInfo, error) {
	obj, gvk, err := codecs.UniversalDeserializer().Decode(raw, nil, nil)
	if err != nil {
		return nil, err
	}
	replicas, err := replicas(obj, gvk)
	if err != nil {
		return nil, err
	}
	return &objectInfo{obj, gvk, replicas}, nil
}

// Verify that the replicas change is a downscale and not an upscale, otherwise allow the change.
// Returns new replicas, if available.
func checkReplicasChange(logger log.Logger, oldInfo, newInfo *objectInfo) (int32, *v1.AdmissionResponse) {
	// Both replicas are nil, nothing to warn about.
	if oldInfo.replicas == nil && newInfo.replicas == nil {
		level.Debug(logger).Log("msg", "no replicas change, allowing")
		return -1, &v1.AdmissionResponse{Allowed: true}
	}
	// Changes from/to nil scale are not downscales strictly speaking.
	if newInfo.replicas == nil {
		return -1, allowWarn(logger, "new replicas is nil, allowing the change")
	}
	if oldInfo.replicas == nil {
		return *newInfo.replicas, allowWarn(logger, "new replicas is nil, allowing the change")
	}
	// If it's not a downscale, just log debug.
	if *oldInfo.replicas < *newInfo.replicas {
		level.Debug(logger).Log("msg", "upscale allowed")
		return *newInfo.replicas, &v1.AdmissionResponse{Allowed: true}
	}
	if *oldInfo.replicas == *newInfo.replicas {
		level.Debug(logger).Log("msg", "no replicas change, allowing")
		return *newInfo.replicas, &v1.AdmissionResponse{Allowed: true}
	}
	// If none of the above conditions are met, it's a downscale.
	return *newInfo.replicas, nil
}

func getLabelsAndAnnotations(ctx context.Context, ar v1.AdmissionReview, api kubernetes.Interface, info *objectInfo) (map[string]string, map[string]string, error) {
	var lbls, annotations map[string]string
	var err error

	switch o := info.obj.(type) {
	case *appsv1.Deployment:
		lbls = o.Labels
		annotations = o.Annotations
	case *appsv1.StatefulSet:
		lbls = o.Labels
		annotations = o.Annotations
	case *appsv1.ReplicaSet:
		lbls = o.Labels
		annotations = o.Annotations
	case *autoscalingv1.Scale:
		lbls, err = getResourceLabels(ctx, ar, api)
		if err != nil {
			return nil, nil, err
		}
		annotations, err = getResourceAnnotations(ctx, ar, api)
		if err != nil {
			return nil, nil, err
		}
	default:
		return nil, nil, fmt.Errorf("unsupported type %T", o)
	}

	return lbls, annotations, nil
}

func createEndpoints(ar v1.AdmissionReview, oldInfo, newInfo *objectInfo, port, path string) []endpoint {
	diff := *oldInfo.replicas - *newInfo.replicas
	eps := make([]endpoint, diff)

	// The DNS entry for a pod of a stateful set is
	// ingester-zone-a-0.$(servicename).$(namespace).svc.cluster.local
	// The service in this case is ingester-zone-a as well.
	// https://kubernetes.io/docs/concepts/workloads/controllers/statefulset/#stable-network-id

	for i := 0; i < int(diff); i++ {
		index := int(*oldInfo.replicas) - i - 1 // nr in statefulset
		eps[i].namespace = ar.Request.Namespace
		eps[i].podName = fmt.Sprintf("%v-%v", ar.Request.Name, index)
		eps[i].url = fmt.Sprintf("%s.%v.%v.svc.cluster.local:%s/%s",
			eps[i].podName,  // pod name
			ar.Request.Name, // svc name
			eps[i].namespace,
			port,
			path,
		)
		eps[i].index = index
	}

	return eps
}

func sendPrepareShutdownRequests(ctx context.Context, logger log.Logger, client httpClient, eps []endpoint, useAndUpdateLastPrepareDownscaleTimestamp bool, api kubernetes.Interface) error {
	if len(eps) == 0 {
		return nil
	}

	span, ctx := opentracing.StartSpanFromContext(ctx, "admission.sendPrepareShutdownRequests()")
	defer span.Finish()

	g, _ := errgroup.WithContext(ctx)
	for _, ep := range eps {
		ep := ep // https://golang.org/doc/faq#closures_and_goroutines
		g.Go(func() error {
			logger, ctx := spanlogger.New(ctx, logger, "admission.PreparePodForShutdown", tenantResolver)
			defer logger.Span.Finish()

			logger.SetSpanAndLogTag("pod", ep.podName)
			logger.SetSpanAndLogTag("url", ep.url)
			logger.SetSpanAndLogTag("index", ep.index)

			if useAndUpdateLastPrepareDownscaleTimestamp {
				t, err := getLastPrepareDownscaleAnnotationOnPod(ctx, api, ep.namespace, ep.podName)
				if err != nil {
					level.Warn(logger).Log("msg", "failed to get pod annotations, continuing with prepare-shutdown", "err", err)
					// Don't return error. prepare-shutdown is expected to be idempotent, so it's OK to call it multiple times.
				} else if !t.IsZero() {
					level.Info(logger).Log("msg", "prepare-shutdown endpoint was already called on pod, will not call it again", "pod", ep.podName, "lastPrepareDownscale", t.Format(time.RFC3339))
					return nil
				}
			}

			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+ep.url, nil)
			if err != nil {
				level.Error(logger).Log("msg", "error creating HTTP POST request", "err", err)
				return err
			}

			req.Header.Set("Content-Type", "application/json")
			req, ht := nethttp.TraceRequest(opentracing.GlobalTracer(), req)
			defer ht.Finish()

			resp, err := client.Do(req)
			if err != nil {
				level.Error(logger).Log("msg", "error sending HTTP POST request", "err", err)
				return err
			}

			defer resp.Body.Close()

			if resp.StatusCode/100 != 2 {
				err := errors.New("HTTP POST request returned non-2xx status code")
				body, readError := io.ReadAll(resp.Body)
				level.Error(logger).Log("msg", "error received from shutdown endpoint", "err", err, "status", resp.StatusCode, "response_body", string(body))
				return errors.Join(err, readError)
			}
			level.Debug(logger).Log("msg", "pod prepared for shutdown")

			if useAndUpdateLastPrepareDownscaleTimestamp {
				err := setLastPrepareDownscaleAnnotationOnPod(ctx, api, ep.namespace, ep.podName, time.Now())
				if err != nil {
					level.Warn(logger).Log("msg", fmt.Sprintf("failed to set %v annotation on the pod", config.LastPrepareDownscaleAnnotationKey), "err", err)
					// Don't return error. In the worst case, we will call prepare-shutdown on this pod again.
				}
			}
			return nil
		})
	}
	return g.Wait()
}

var tenantResolver spanlogger.TenantResolver = noTenantResolver{}

type noTenantResolver struct{}

func (n noTenantResolver) TenantID(ctx context.Context) (string, error) {
	return "", nil
}

func (n noTenantResolver) TenantIDs(ctx context.Context) ([]string, error) {
	return nil, nil
}

func getLastPrepareDownscaleAnnotationOnPod(ctx context.Context, api kubernetes.Interface, namespace, podName string) (time.Time, error) {
	anns, err := getPodAnnotations(ctx, api, namespace, podName)
	if err != nil {
		return time.Time{}, err
	}

	last := anns[config.LastPrepareDownscaleAnnotationKey]
	if last == "" {
		return time.Time{}, nil
	}

	return time.Parse(time.RFC3339, last)
}

func setLastPrepareDownscaleAnnotationOnPod(ctx context.Context, api kubernetes.Interface, namespace, podName string, t time.Time) error {
	return setPodAnnotation(ctx, api, namespace, podName, config.LastPrepareDownscaleAnnotationKey, t.UTC().Format(time.RFC3339))
}

func getPodAnnotations(ctx context.Context, api kubernetes.Interface, namespace, podName string) (map[string]string, error) {
	span, ctx := opentracing.StartSpanFromContext(ctx, "admission.getPodAnnotation()")
	defer span.Finish()

	span.SetTag("object.namespace", namespace)
	span.SetTag("object.name", podName)

	pod, err := api.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	return pod.Annotations, nil
}

func setPodAnnotation(ctx context.Context, api kubernetes.Interface, namespace, podName string, annotationName, annotationValue string) error {
	span, ctx := opentracing.StartSpanFromContext(ctx, "admission.addPodAnnotation()")
	defer span.Finish()

	span.SetTag("object.namespace", namespace)
	span.SetTag("object.name", podName)

	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{annotationName: annotationValue}}})
	if err != nil {
		return fmt.Errorf("failed to encode annotations patch: %v", err)
	}

	client := api.CoreV1().Pods(namespace)
	_, err = client.Patch(ctx, podName, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	return err
}
