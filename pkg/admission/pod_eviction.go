package admission

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/spanlogger"
	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/grafana/rollout-operator/pkg/util"
	v1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const (
	PodEvictionWebhookPath = "/admission/pod-eviction"

	eviction = "eviction"

	// labels we use in logging
	logMsg          = "msg"
	logStatefulSet  = "sts"
	logLabel        = "label"
	logErr          = "err"
	logPod          = "pod"
	logZones        = "zones"
	logPartition    = "partition"
	logUnder        = "under"
	logUnknown      = "unknown"
	logAllow        = "pod eviction allowed"
	logDeny         = "pod eviction denied"
	logUnavailable0 = "max unavailable = 0"
)

// A partitionMatcher is a utility to assist in matching pods to a partition.
type partitionMatcher struct {
	same func(pod *corev1.Pod) bool
}

// plural appends an 's' to the given string if the value is > 1.
func plural(s string, value int) string {
	if value > 1 {
		return fmt.Sprintf("%ss", s)
	}
	return s
}

// A admissionReviewRequest holds the context of the request we are processing.
type admissionReviewRequest struct {
	log     *spanlogger.SpanLogger
	req     v1.AdmissionReview
	clients k8sClients
}

// validate ensures that the AdmissionRequest contains the information we require to process this request.
func (r *admissionReviewRequest) validate() error {
	// ignore non-create operations
	if r.req.Request.Operation != v1.Create {
		return errors.New("request operation is not create")
	}

	// ignore create operations other than subresource eviction
	if r.req.Request.SubResource != eviction {
		return errors.New(`request SubResource is not eviction`)
	}

	// we won't be able to determine the eviction pod
	if (r.req.Request == nil || len(r.req.Request.Namespace) == 0) || len(r.req.Request.Name) == 0 {
		return errors.New("namespace or name are not found")
	}

	return nil
}

// initLogger initialises the logger with context of this request.
func (r *admissionReviewRequest) initLogger() {
	// this is the name of the pod
	r.log.SetSpanAndLogTag("object.name", r.req.Request.Name)
	r.log.SetSpanAndLogTag("object.resource", r.req.Request.Resource.Resource)
	r.log.SetSpanAndLogTag("object.namespace", r.req.Request.Namespace)
	// note that this is the UID of the request, not of the pod
	r.log.SetSpanAndLogTag("request.uid", r.req.Request.UID)

	if r.req.Request.DryRun != nil {
		r.log.SetSpanAndLogTag("request.dry_run", r.req.Request.DryRun)
	}
}

// denyWithReason constructs a denied AdmissionResponse with the given reason included in the response warnings attribute.
func (r *admissionReviewRequest) denyWithReason(reason string) *v1.AdmissionResponse {
	rsp := v1.AdmissionResponse{Allowed: false}
	rsp.Warnings = append(rsp.Warnings, reason)
	return &rsp
}

// allowWithReason constructs an allowed AdmissionResponse with the given reason included in the response warnings attribute.
func (r *admissionReviewRequest) allowWithReason(reason string) *v1.AdmissionResponse {
	rsp := v1.AdmissionResponse{Allowed: true}
	rsp.Warnings = append(rsp.Warnings, reason)
	return &rsp
}

// PodEviction is the entry point / handler for the pod-eviction endpoint request.
// Note - we do not perform anything different for dry-run requests (ar.Request.DryRun) as we do not modify any state in this webhook.
func PodEviction(ctx context.Context, l log.Logger, ar v1.AdmissionReview, kubeClient kubernetes.Interface, dynamicClient dynamic.Interface) *v1.AdmissionResponse {
	logger, ctx := spanlogger.New(ctx, l, "admission.PodEviction()", tenantResolver)
	defer logger.Finish()

	var pod *corev1.Pod
	var sts *appsv1.StatefulSet
	var err error

	request := admissionReviewRequest{req: ar, log: logger, clients: k8sClients{ctx: ctx, kubeClient: kubeClient, dynamicClient: dynamicClient}}

	// set up key=value pairs we include on all logs within this context
	request.initLogger()

	// validate that this is a valid pod eviction create request
	if err = request.validate(); err != nil {
		_ = level.Error(request.log).Log(logMsg, fmt.Sprintf("%s - not a valid create pod eviction request", logAllow), logErr, err)
		return request.allowWithReason(err.Error())
	}

	// get the pod which has been requested for eviction
	if pod, err = request.clients.podByName(request.req.Request.Namespace, request.req.Request.Name); err != nil {
		_ = level.Error(request.log).Log(logMsg, fmt.Sprintf("%s - unable to find pod by name", logAllow))
		return request.allowWithReason(err.Error())
	}

	// if the pod has not yet reached a running state or has already been disrupted then we don't deny this eviction request
	// TODO - check on this
	if !util.IsPodRunningAndReady(pod) {
		_ = level.Info(request.log).Log(logMsg, fmt.Sprintf("%s - pod is not ready", logAllow))
		return request.allowWithReason("pod is not ready")
	}

	// rollout-group label is needed to find a matching custom resource which is used for configuration
	if rolloutGroup, found := pod.Labels[config.RolloutGroupLabelKey]; !found || len(rolloutGroup) == 0 {
		_ = level.Error(request.log).Log(logMsg, fmt.Sprintf("%s - unable to find required label on pod", logAllow), logLabel, config.RolloutGroupLabelKey)
		return request.allowWithReason("unable to find label on pod")
	}

	// ie ingester
	request.log.SetSpanAndLogTag(config.RolloutGroupLabelKey, pod.Labels[config.RolloutGroupLabelKey])

	// get the StatefulSet who manages this pod
	if sts, err = request.clients.owner(pod); err != nil {
		_ = level.Error(request.log).Log(logMsg, fmt.Sprintf("%s - unable to find pod owner", logAllow), logErr, err)
		return request.allowWithReason("unable to find a StatefulSet as pod owner")
	}

	// ie ingester-zone-a
	request.log.SetSpanAndLogTag("owner", sts.Name)

	PdbConfig, err := config.GetCustomResourceConfig(request.clients.ctx, request.req.Request.Namespace, pod.Labels[config.RolloutGroupLabelKey], request.clients.dynamicClient, request.log)
	if err != nil {
		return request.allowWithReason(err.Error())
	}

	// the number of allowed unavailable pods in other zones.
	maxUnavailable := PdbConfig.MaxUnavailablePods(sts)
	if maxUnavailable == 0 {
		_ = level.Info(request.log).Log(logMsg, fmt.Sprintf("%s - %s", logDeny, logUnavailable0))
		return request.denyWithReason(logUnavailable0)
	}

	// Find the other StatefulSets which span all zones.
	// Assumption - each StatefulSet manages all the pods for a single zone. This list of StatefulSets covers all zones.
	// During a migration it may be possible to break this assumption, but during a migration the maxUnavailable will be set to 0 and the eviction request will be denied.
	otherStatefulSets, err := request.clients.findRelatedStatefulSets(sts, PdbConfig.StsSelector())
	if err != nil || otherStatefulSets == nil || len(otherStatefulSets.Items) <= 1 {
		_ = level.Error(request.log).Log(logMsg, "unable to find related stateful sets - continuing with single zone")
		otherStatefulSets = &appsv1.StatefulSetList{Items: []appsv1.StatefulSet{*sts}}
	}

	// this is the partition of the pod being evicted - for classic zones the partition will be an empty string
	partition := PdbConfig.PodPartition(pod)
	matcher := partitionMatcher{
		same: func(pd *corev1.Pod) bool {
			return PdbConfig.PodPartition(pd) == partition
		},
	}

	// include the partition we are considering
	if len(partition) > 0 {
		request.log.SetSpanAndLogTag(logPartition, partition)
	}

	// include the number of zones we will be considering
	request.log.SetSpanAndLogTag(logZones, len(otherStatefulSets.Items))

	singleZone := len(otherStatefulSets.Items) == 1

	for _, otherSts := range otherStatefulSets.Items {
		// exclude our StatefulSet from this iteration unless we only have found a single zone
		if !singleZone && otherSts.UID == sts.UID {
			continue
		}

		// find the number of not ready pods on this StatefulSet which match the given criteria
		result, err := request.clients.podsNotRunningAndReady(&otherSts, pod, matcher)
		if err != nil {
			// TODO - should we allow or deny this ??
			_ = level.Error(request.log).Log(logMsg, "unable to determine pod status in related StatefulSet", logStatefulSet, otherSts.Name)
			continue
		}

		// Note - if we are in a singleZone mode then we apply the PDB to the single zone and the unavailable pods calculation reflects the state after eviction
		if result.notReady+result.unknown >= maxUnavailable || (singleZone && result.notReady+result.unknown+1 > maxUnavailable) {
			_ = level.Info(request.log).Log(logMsg, fmt.Sprintf("%s - pdb exceeded", logDeny), logStatefulSet, otherSts.Name, "notReady", result.notReady, logUnknown, result.unknown, "tested", result.tested, "maxUnavailable", maxUnavailable)

			// Build a nice reason to assist with any debugging
			msg := ""
			if result.notReady > 0 {
				msg += fmt.Sprintf("%d %s not ready", result.notReady, plural(logPod, result.notReady))
				if result.unknown > 0 {
					msg += ", "
				}
			}
			if result.unknown > 0 {
				// 1 pod not ready, 1 pod unknown under ingester-zone-a
				// 1 pod unknown under ingester-zone-a
				msg += fmt.Sprintf("%d %s %s %s %s", result.unknown, plural(logPod, result.unknown), logUnknown, logUnder, otherSts.Name)
			} else if len(partition) > 0 {
				// 1 pod not ready under ingester-zone-a partition 0
				msg += fmt.Sprintf(" %s %s %s %s", logUnder, otherSts.Name, logPartition, partition)
			} else {
				// 1 pod not ready under ingester-zone-a
				msg += fmt.Sprintf(" %s %s", logUnder, otherSts.Name)
			}

			return request.denyWithReason(msg)
		}

	}

	// a string we will use when logging
	scope := logZones
	if len(partition) > 0 {
		scope += fmt.Sprintf(" %s %s", logPartition, partition)
	}

	// all relevant pods in adjacent zones are available
	// all relevant pods in adjacent zones partition 1 are available
	msg := fmt.Sprintf("all relevant pods in adjacent %s are available", scope)
	_ = level.Info(request.log).Log(logMsg, msg)
	return request.allowWithReason(msg)
}
