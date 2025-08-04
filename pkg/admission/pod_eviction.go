package admission

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/spanlogger"
	v1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/grafana/rollout-operator/pkg/util"
)

const (
	PodEvictionWebhookPath = "/admission/pod-eviction"

	eviction = "eviction"

	// labels we use in logging
	logMsg   = "msg"
	logAllow = "pod eviction allowed"
	logDeny  = "pod eviction denied"
)

// A partitionMatcher is a utility to assist in matching pods to a partition.
type partitionMatcher struct {
	same func(pod *corev1.Pod) bool
}

// A pdbValidator abstracts the different PDB implementations
type pdbValidator struct {

	// considerSts returns true if this StatefulSet should be considered in the PDB tallies
	considerSts func(sts *appsv1.StatefulSet) bool

	// considerPod returns a matcher which is used to test if a pod should be considered in the PDB tallies
	considerPod partitionMatcher

	// validate validates that the PDB will not be breached across all zones
	validate func(result *podStatusResult, maxUnavailable int) error

	// successMessage returns a success message we return in the eviction response
	successMessage func() string
}

// plural appends an 's' to the given string if the value is > 1.
func plural(s string, value int) string {
	if value > 1 {
		return fmt.Sprintf("%ss", s)
	}
	return s
}

// pdbMessage creates a message which includes the number of not ready and unknown pods within the given span
func pdbMessage(result *podStatusResult, span string) string {
	msg := ""
	if result.notReady > 0 {
		// 1 pod not ready
		msg += fmt.Sprintf("%d %s not ready", result.notReady, plural("pod", result.notReady))
		if result.unknown > 0 {
			msg += ", "
		}
	}
	if result.unknown > 0 {
		// 2 pods unknown
		msg += fmt.Sprintf("%d %s unknown", result.unknown, plural("pod", result.unknown))
	}

	// under ingester-zone-a partition 0
	// under ingester-zone-a
	if len(span) > 0 {
		msg += fmt.Sprintf(" under %s", span)
	}
	return msg
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
		level.Error(request.log).Log(logMsg, fmt.Sprintf("%s - not a valid create pod eviction request", logAllow), "err", err)
		return request.allowWithReason(err.Error())
	}

	// get the pod which has been requested for eviction
	if pod, err = request.clients.podByName(request.req.Request.Namespace, request.req.Request.Name); err != nil {
		level.Error(request.log).Log(logMsg, fmt.Sprintf("%s - unable to find pod by name", logAllow))
		return request.allowWithReason(err.Error())
	}

	// if the pod has not yet reached a running state or has already been disrupted then we don't deny this eviction request
	// TODO - check on this
	if !util.IsPodRunningAndReady(pod) {
		level.Info(request.log).Log(logMsg, fmt.Sprintf("%s - pod is not ready", logAllow))
		return request.allowWithReason("pod is not ready")
	}

	// rollout-group label is needed to find a matching custom resource which is used for configuration
	if rolloutGroup, found := pod.Labels[config.RolloutGroupLabelKey]; !found || len(rolloutGroup) == 0 {
		level.Error(request.log).Log(logMsg, fmt.Sprintf("%s - unable to find required label on pod", logAllow), "label", config.RolloutGroupLabelKey)
		return request.allowWithReason("unable to find label on pod")
	}

	// ie ingester
	request.log.SetSpanAndLogTag(config.RolloutGroupLabelKey, pod.Labels[config.RolloutGroupLabelKey])

	// get the StatefulSet who manages this pod
	if sts, err = request.clients.owner(pod); err != nil {
		level.Error(request.log).Log(logMsg, fmt.Sprintf("%s - unable to find pod owner", logAllow), "err", err)
		return request.allowWithReason(err.Error())
	}

	// ie ingester-zone-a
	request.log.SetSpanAndLogTag("owner", sts.Name)

	// RolloutGroupLabelKey --> ingester
	pdbConfig, err := config.GetCustomResourceConfig(request.clients.ctx, request.req.Request.Namespace, pod.Labels[config.RolloutGroupLabelKey], request.clients.dynamicClient, request.log)
	if err != nil {
		return request.allowWithReason(err.Error())
	}

	// the number of allowed unavailable pods in other zones.
	maxUnavailable := pdbConfig.MaxUnavailablePods(sts)
	if maxUnavailable == 0 {
		level.Info(request.log).Log(logMsg, fmt.Sprintf("%s - max unavailable = 0", logDeny))
		return request.denyWithReason("max unavailable = 0")
	}

	// Find the other StatefulSets which span all zones.
	// Assumption - each StatefulSet manages all the pods for a single zone. This list of StatefulSets covers all zones.
	// During a migration it may be possible to break this assumption, but during a migration the maxUnavailable will be set to 0 and the eviction request will be denied.
	otherStatefulSets, err := request.clients.findRelatedStatefulSets(sts, pdbConfig.StsSelector())
	if err != nil || otherStatefulSets == nil || len(otherStatefulSets.Items) <= 1 {
		level.Error(request.log).Log(logMsg, "unable to find related stateful sets - continuing with single zone")
		otherStatefulSets = &appsv1.StatefulSetList{Items: []appsv1.StatefulSet{*sts}}
	}

	// this is the partition of the pod being evicted - for classic zones the partition will be an empty string
	partition, err := pdbConfig.PodPartition(pod)
	if err != nil {
		return request.denyWithReason(err.Error())
	}

	// include the partition we are considering
	if len(partition) > 0 {
		request.log.SetSpanAndLogTag("partition", partition)
	}

	// include the number of zones we will be considering
	request.log.SetSpanAndLogTag("zones", len(otherStatefulSets.Items))

	var pdb *pdbValidator

	if len(otherStatefulSets.Items) == 1 {
		// single zone mode - we include the pod being evicted into the calculation and the maxAvailable is applied to the single zone / StatefulSet
		pdb = makePdbValidatorSingleZone(&otherStatefulSets.Items[0])
	} else if len(partition) > 0 {
		// partition mode - the pod tallies are applied to all pods in other zones which relate to this partition
		pdb = makePdbValidatorPartition(sts, partition, len(otherStatefulSets.Items), pdbConfig, request.log)
	} else {
		// zone mode - each zone is individually evaluated against the pdb
		pdb = makePdbValidatorZones(sts, len(otherStatefulSets.Items))
	}

	accumulatedResult := &podStatusResult{}
	for _, otherSts := range otherStatefulSets.Items {
		// test on whether we exclude the eviction pod's StatefulSet from the tally
		if !pdb.considerSts(&otherSts) {
			continue
		}

		result, err := request.clients.podsNotRunningAndReady(&otherSts, pod, pdb.considerPod)
		if err != nil {
			level.Error(request.log).Log(logMsg, fmt.Sprintf("%s - unable to determine pod status in related StatefulSet", logDeny), "sts", otherSts.Name)
			return request.denyWithReason("unable to determine pod status in related StatefulSet")
		}

		accumulatedResult.tested += result.tested
		accumulatedResult.notReady += result.notReady
		accumulatedResult.unknown += result.unknown
	}

	if err = pdb.validate(accumulatedResult, maxUnavailable); err != nil {
		level.Error(request.log).Log(logMsg, fmt.Sprintf("%s - pdb exceeded", logDeny), "err", err)
		return request.denyWithReason(err.Error())
	}

	return request.allowWithReason(pdb.successMessage())
}

// makePdbValidatorSingleZone returns a pdbValidator which evaluates the PDB for a single zone / StatefulSet
func makePdbValidatorSingleZone(sts *appsv1.StatefulSet) *pdbValidator {

	pdb := pdbValidator{
		considerSts: func(sts *appsv1.StatefulSet) bool {
			return true
		},
		validate: func(result *podStatusResult, maxUnavailable int) error {
			// add 1 to reflect the pod which is being requested for eviction
			if result.notReady+result.unknown+1 > maxUnavailable {
				return errors.New(pdbMessage(result, sts.Name))
			}
			return nil
		},
		successMessage: func() string {
			return fmt.Sprintf("pdb met in single zone %s", sts.Name)
		},
		considerPod: partitionMatcher{
			same: func(pd *corev1.Pod) bool {
				return true
			},
		},
	}
	return &pdb
}

func makePdbValidatorPartition(sts *appsv1.StatefulSet, partition string, zones int, pdbConfig *config.PdbConfig, log *spanlogger.SpanLogger) *pdbValidator {

	// partition mode - we apply the unavailable tally across all zones which relate to this partition
	pdb := pdbValidator{
		considerSts: func(otherSts *appsv1.StatefulSet) bool {
			return otherSts.UID != sts.UID
		},
		validate: func(result *podStatusResult, maxUnavailable int) error {
			if result.notReady+result.unknown >= maxUnavailable {
				return errors.New(pdbMessage(result, "partition "+partition))
			}
			return nil
		},
		successMessage: func() string {
			return fmt.Sprintf("pdb met for partition %s across %d zones", partition, zones)
		},
		considerPod: partitionMatcher{
			same: func(pd *corev1.Pod) bool {
				thisPartition, err := pdbConfig.PodPartition(pd)
				if err != nil {
					// the partition name was successfully extracted from the pod being evicted
					// so if this regex has failed then the assumption is that it is not the same partition, as would have a different naming convention
					// or the regex is too tightly defined
					level.Error(log).Log(logMsg, "Unable to extract partition from pod name - check the pod partition name regex", "name", pd.Name)
				}
				return thisPartition == partition
			},
		},
	}

	return &pdb
}

func makePdbValidatorZones(sts *appsv1.StatefulSet, zones int) *pdbValidator {
	pdb := pdbValidator{
		considerSts: func(otherSts *appsv1.StatefulSet) bool {
			return otherSts.UID != sts.UID
		},
		validate: func(result *podStatusResult, maxUnavailable int) error {
			if result.notReady+result.unknown >= maxUnavailable {
				return errors.New(pdbMessage(result, ""))
			}
			return nil
		},
		successMessage: func() string {
			return fmt.Sprintf("pdb met across %d zones", zones)
		},
		considerPod: partitionMatcher{
			same: func(pd *corev1.Pod) bool {
				return true
			},
		},
	}
	return &pdb
}
