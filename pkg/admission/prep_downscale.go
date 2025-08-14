package admission

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/spanlogger"
	"go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"
	admissionv1 "k8s.io/api/admission/v1"
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
	maxPrepareGoroutines        = 32
)

func PrepareDownscale(ctx context.Context, rt http.RoundTripper, logger log.Logger, ar admissionv1.AdmissionReview, api *kubernetes.Clientset, useZoneTracker bool, zoneTrackerConfigMapName string, clusterDomain string) *admissionv1.AdmissionResponse {
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: otelhttp.NewTransport(rt, otelhttp.WithClientTrace(func(ctx context.Context) *httptrace.ClientTrace {
			return otelhttptrace.NewClientTrace(ctx)
		})),
	}

	if useZoneTracker {
		zt := newZoneTracker(api, clusterDomain, ar.Request.Namespace, zoneTrackerConfigMapName)
		return zt.prepareDownscale(ctx, logger, ar, api, client)
	}

	return prepareDownscale(ctx, logger, ar, api, client, clusterDomain)
}

type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

func prepareDownscale(ctx context.Context, l log.Logger, ar admissionv1.AdmissionReview, api kubernetes.Interface, client httpClient, clusterDomain string) *admissionv1.AdmissionResponse {
	logger, ctx := spanlogger.New(ctx, l, "admission.prepareDownscale()", tenantResolver)
	defer logger.Finish()

	logger.SetSpanAndLogTag("object.name", ar.Request.Name)
	logger.SetSpanAndLogTag("object.resource", ar.Request.Resource.Resource)
	logger.SetSpanAndLogTag("object.namespace", ar.Request.Namespace)
	logger.SetSpanAndLogTag("request.dry_run", *ar.Request.DryRun)
	logger.SetSpanAndLogTag("request.uid", ar.Request.UID)

	if *ar.Request.DryRun {
		return &admissionv1.AdmissionResponse{Allowed: true}
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
	response := checkReplicasChange(logger, oldInfo, newInfo)
	if response != nil {
		return response
	}

	stsPrepareInfo, err := getStatefulSetPrepareInfo(ctx, ar, api, oldInfo)
	if err != nil {
		return allowWarn(logger, fmt.Sprintf("%s, allowing the change", err))
	}

	// Since it's a downscale, check if the resource needs to be prepared to be downscaled.
	if !stsPrepareInfo.prepareDownscale {
		// Nothing to do.
		return &admissionv1.AdmissionResponse{Allowed: true}
	}

	if stsPrepareInfo.port == "" {
		level.Warn(logger).Log("msg", fmt.Sprintf("downscale not allowed because the %v annotation is not set or empty", config.PrepareDownscalePortAnnotationKey))
		return deny(
			fmt.Sprintf(
				"downscale of %s/%s in %s from %d to %d replicas is not allowed because the %v annotation is not set or empty.",
				ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas, config.PrepareDownscalePortAnnotationKey,
			),
		)
	}

	if stsPrepareInfo.path == "" {
		level.Warn(logger).Log("msg", fmt.Sprintf("downscale not allowed because the %v annotation is not set or empty", config.PrepareDownscalePathAnnotationKey))
		return deny(
			fmt.Sprintf(
				"downscale of %s/%s in %s from %d to %d replicas is not allowed because the %v annotation is not set or empty.",
				ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas, config.PrepareDownscalePathAnnotationKey,
			),
		)
	}

	if stsPrepareInfo.serviceName == "" {
		level.Warn(logger).Log("msg", "downscale not allowed because the serviceName is not set or empty")
		return deny(
			fmt.Sprintf(
				"downscale of %s/%s in %s from %d to %d replicas is not allowed because the serviceName is not set or empty.",
				ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas,
			),
		)
	}

	if stsPrepareInfo.rolloutGroup != "" {
		stsList, err := findStatefulSetsForRolloutGroup(ctx, api, ar.Request.Namespace, stsPrepareInfo.rolloutGroup)
		if err != nil {
			level.Warn(logger).Log("msg", "downscale not allowed due to error while finding other statefulsets", "err", err)
			return deny(
				fmt.Sprintf(
					"downscale of %s/%s in %s from %d to %d replicas is not allowed because finding other statefulsets failed.",
					ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas,
				),
			)
		}
		foundSts, err := findDownscalesDoneMinTimeAgo(stsList, ar.Request.Name)
		if err != nil {
			level.Warn(logger).Log("msg", "downscale not allowed due to error while parsing downscale annotations", "err", err)
			return deny(
				fmt.Sprintf(
					"downscale of %s/%s in %s from %d to %d replicas is not allowed because parsing downscale annotations failed.",
					ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas,
				),
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

	// It's a downscale, so we need to prepare the pods that are going away for shutdown.
	eps := createEndpoints(ar, oldInfo, newInfo, stsPrepareInfo.port, stsPrepareInfo.path, stsPrepareInfo.serviceName, clusterDomain)

	if err := sendPrepareShutdownRequests(ctx, logger, client, eps); err != nil {
		// Down-scale operation is disallowed because at least one pod failed to
		// prepare for shutdown and cannot be deleted. We also need to
		// un-prepare them all.

		level.Error(logger).Log("msg", "downscale not allowed due to host(s) failing to prepare for downscale. unpreparing...", "err", err)
		undoPrepareShutdownRequests(ctx, logger, client, eps)

		return deny(
			fmt.Sprintf(
				"downscale of %s/%s in %s from %d to %d replicas is not allowed because one or more pods failed to prepare for shutdown.",
				ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas,
			),
		)
	}

	if err := addDownscaledAnnotationToStatefulSet(ctx, api, ar.Request.Namespace, ar.Request.Name); err != nil {
		// Down-scale operation is disallowed because we failed to add the
		// annotation to the statefulset. We again need to un-prepare all pods.
		level.Error(logger).Log("msg", "downscale not allowed due to error while adding annotation. unpreparing...", "err", err)
		undoPrepareShutdownRequests(ctx, logger, client, eps)

		return deny(
			fmt.Sprintf(
				"downscale of %s/%s in %s from %d to %d replicas is not allowed because adding an annotation to the statefulset failed.",
				ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas,
			),
		)
	}

	// Otherwise, we've made it through the gauntlet, and the downscale is allowed.
	level.Info(logger).Log("msg", "downscale allowed")
	return &admissionv1.AdmissionResponse{
		Allowed: true,
		Result: &metav1.Status{
			Message: fmt.Sprintf("downscale of %s/%s in %s from %d to %d replicas is allowed -- all pods successfully prepared for shutdown.", ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas),
		},
	}
}

type statefulSetPrepareInfo struct {
	prepareDownscale bool
	port             string
	path             string
	rolloutGroup     string
	serviceName      string
}

func getStatefulSetPrepareInfo(ctx context.Context, ar admissionv1.AdmissionReview, api kubernetes.Interface, info *objectInfo) (*statefulSetPrepareInfo, error) {
	var sts *appsv1.StatefulSet
	switch o := info.obj.(type) {
	case *appsv1.StatefulSet:
		sts = o
	case *autoscalingv1.Scale:
		var err error
		sts, err = getStatefulSet(ctx, ar, api)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported type %s (go type %T)", info.gvk, info.obj)
	}

	return &statefulSetPrepareInfo{
		prepareDownscale: sts.Labels[config.PrepareDownscaleLabelKey] == config.PrepareDownscaleLabelValue,
		port:             sts.Annotations[config.PrepareDownscalePortAnnotationKey],
		path:             sts.Annotations[config.PrepareDownscalePathAnnotationKey],
		rolloutGroup:     sts.Labels[config.RolloutGroupLabelKey],
		serviceName:      sts.Spec.ServiceName,
	}, nil
}

// deny returns a *v1.AdmissionResponse with Allowed: false and the message provided
func deny(msg string) *admissionv1.AdmissionResponse {
	return &admissionv1.AdmissionResponse{
		Allowed: false,
		Result: &metav1.Status{
			Message: msg,
		},
	}
}

func getStatefulSet(ctx context.Context, ar admissionv1.AdmissionReview, api kubernetes.Interface) (*appsv1.StatefulSet, error) {
	ctx, span := tracer.Start(ctx, "admission.getStatefulSet()", trace.WithAttributes(
		attribute.String("object.namespace", ar.Request.Namespace),
		attribute.String("object.name", ar.Request.Name),
	))
	defer span.End()

	switch ar.Request.Resource.Resource {
	case "statefulsets":
		obj, err := api.AppsV1().StatefulSets(ar.Request.Namespace).Get(ctx, ar.Request.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return obj, nil
	}
	return nil, fmt.Errorf("unsupported resource %s", ar.Request.Resource.Resource)
}

func addDownscaledAnnotationToStatefulSet(ctx context.Context, api kubernetes.Interface, namespace, stsName string) error {
	ctx, span := tracer.Start(ctx, "admission.addDownscaledAnnotationToStatefulSet()", trace.WithAttributes(
		attribute.String("object.namespace", namespace),
		attribute.String("object.name", stsName),
	))
	defer span.End()

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
	ctx, span := tracer.Start(ctx, "admission.findStatefulSetWithNonUpdatedReplicas()", trace.WithAttributes(
		attribute.String("object.namespace", namespace),
	))
	defer span.End()

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
	ctx, span := tracer.Start(ctx, "admission.countRunningAndReadyPods()", trace.WithAttributes(
		attribute.String("object.namespace", namespace),
		attribute.String("object.name", sts.Name),
	))
	defer span.End()

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
	ctx, span := tracer.Start(ctx, "admission.findStatefulSetsForRolloutGroup()", trace.WithAttributes(
		attribute.String("object.namespace", namespace),
		attribute.String("rollout_group", rolloutGroup),
	))
	defer span.End()

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
	url   string
	index int
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

// Verify that the replicas change is a downscale and not an upscale, otherwise allow the change
func checkReplicasChange(logger log.Logger, oldInfo, newInfo *objectInfo) *admissionv1.AdmissionResponse {
	// Both replicas are nil, nothing to warn about.
	if oldInfo.replicas == nil && newInfo.replicas == nil {
		level.Debug(logger).Log("msg", "no replicas change, allowing")
		return &admissionv1.AdmissionResponse{Allowed: true}
	}
	// Changes from/to nil scale are not downscales strictly speaking.
	if oldInfo.replicas == nil || newInfo.replicas == nil {
		return allowWarn(logger, "old/new replicas is nil, allowing the change")
	}
	// If it's not a downscale, just log debug.
	if *oldInfo.replicas < *newInfo.replicas {
		level.Debug(logger).Log("msg", "upscale allowed")
		return &admissionv1.AdmissionResponse{Allowed: true}
	}
	if *oldInfo.replicas == *newInfo.replicas {
		level.Debug(logger).Log("msg", "no replicas change, allowing")
		return &admissionv1.AdmissionResponse{Allowed: true}
	}
	// If none of the above conditions are met, it's a downscale.
	return nil
}

func createEndpoints(ar admissionv1.AdmissionReview, oldInfo, newInfo *objectInfo, port, path, serviceName, clusterDomain string) []endpoint {
	diff := (*oldInfo.replicas - *newInfo.replicas)
	eps := make([]endpoint, diff)

	for i := range int(diff) {
		index := int(*oldInfo.replicas) - i - 1 // nr in statefulset
		eps[i].url = fmt.Sprintf("%s:%s/%s",
			util.StatefulSetPodFQDN(ar.Request.Namespace, ar.Request.Name, index, serviceName, clusterDomain),
			port,
			path,
		)
		eps[i].index = index
	}

	return eps
}

func invokePrepareShutdown(ctx context.Context, method string, parentLogger log.Logger, client httpClient, ep endpoint) error {
	span := "admission.PreparePodForShutdown"
	if method == http.MethodDelete {
		span = "admission.UnpreparePodForShutdown"
	}

	logger, ctx := spanlogger.New(ctx, parentLogger, span, tenantResolver)
	defer logger.Finish()

	logger.SetSpanAndLogTag("url", ep.url)
	logger.SetSpanAndLogTag("index", ep.index)
	logger.SetSpanAndLogTag("method", method)

	req, err := http.NewRequestWithContext(ctx, method, "http://"+ep.url, nil)
	if err != nil {
		level.Error(logger).Log("msg", fmt.Sprintf("error creating HTTP %s request", method), "err", err)
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		level.Error(logger).Log("msg", fmt.Sprintf("error sending HTTP %s request", method), "err", err)
		return err
	}

	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		err := fmt.Errorf("HTTP %s request returned non-2xx status code", method)
		body, readError := io.ReadAll(resp.Body)
		level.Error(logger).Log("msg", "error received from shutdown endpoint", "err", err, "status", resp.StatusCode, "response_body", string(body))
		return errors.Join(err, readError)
	}
	level.Debug(logger).Log("msg", "pod prepare-shutdown handler called", "method", method, "url", ep.url)
	return nil
}

func sendPrepareShutdownRequests(ctx context.Context, logger log.Logger, client httpClient, eps []endpoint) error {
	ctx, span := tracer.Start(ctx, "admission.sendPrepareShutdownRequests()")
	defer span.End()

	if len(eps) == 0 {
		return nil
	}

	// Attempt to POST to every prepare-shutdown endpoint.

	g, ectx := errgroup.WithContext(ctx)
	g.SetLimit(maxPrepareGoroutines)
	for _, ep := range eps {
		ep := ep
		g.Go(func() error {
			if err := ectx.Err(); err != nil {
				return err
			}
			return invokePrepareShutdown(ectx, http.MethodPost, logger, client, ep)
		})
	}

	return g.Wait()
}

// undoPrepareShutdownRequests sends an HTTP DELETE to each of the given endpoints.
func undoPrepareShutdownRequests(ctx context.Context, logger log.Logger, client httpClient, eps []endpoint) {
	ctx, span := tracer.Start(ctx, "admission.undoPrepareShutdownRequests()")
	defer span.End()

	if len(eps) == 0 {
		return
	}

	// Unlike sendPrepareShutdownRequests, we attempt to send each pod a DELETE
	// without regard for failures.

	undoGroup, _ := errgroup.WithContext(ctx)
	undoGroup.SetLimit(maxPrepareGoroutines)
	for _, ep := range eps {
		ep := ep
		undoGroup.Go(func() error {
			if err := invokePrepareShutdown(ctx, http.MethodDelete, logger, client, ep); err != nil {
				level.Warn(logger).Log("msg", "failed to undo prepare shutdown request", "url", ep.url, "err", err)
				// (We swallow the error so all of the deletes are attempted.)
			}
			return nil
		})
	}

	_ = undoGroup.Wait()
}

var tenantResolver spanlogger.TenantResolver = noTenantResolver{}

type noTenantResolver struct{}

func (n noTenantResolver) TenantID(ctx context.Context) (string, error) {
	return "", nil
}

func (n noTenantResolver) TenantIDs(ctx context.Context) ([]string, error) {
	return nil, nil
}
