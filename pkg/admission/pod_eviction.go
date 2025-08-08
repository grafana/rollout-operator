// Provenance-includes-location: https://github.com/kubernetes/kubernetes
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: The Kubernetes Authors.

package admission

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/spanlogger"
	v1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/grafana/rollout-operator/pkg/zpdb"
)

const (
	PodEvictionWebhookPath = "/admission/pod-eviction"

	eviction = "eviction"

	// labels we use in logging
	logMsg   = "msg"
	logAllow = "pod eviction allowed"
	logDeny  = "pod eviction denied"

	// the AdmissionResponse HTTP status codes sent in deny responses
	// PDB has been reached
	httpStatusPdbExceeded = 429
	// maxUnavailable=0
	httpStatusPdbDisabled = 403
	// unable to determine status of pods/statefulsets
	httpStatusInternalError = 500
	// configuration error
	httpStatusMisconfiguration = 400
)

// A partitionMatcher is a utility to assist in matching pods to a partition.
type partitionMatcher struct {
	same func(pod *corev1.Pod) bool
}

// A pdbValidator abstracts the different ZPDB implementations
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

// a lock used to control finding a specific PDB lock
var lock = sync.RWMutex{}

// a map of ZPDB name to lock
var locks = make(map[string]*sync.Mutex, 5)

// findLock returns a Mutex for each unique name, creating the Mutex if required.
func findLock(name string) *sync.Mutex {
	lock.RLock()
	if pdbLock, exists := locks[name]; exists {
		lock.RUnlock()
		return pdbLock
	}
	lock.RUnlock()

	lock.Lock()
	defer lock.Unlock()

	if pdbLock, exists := locks[name]; exists {
		return pdbLock
	}

	locks[name] = &sync.Mutex{}
	return locks[name]
}

// A admissionReviewRequest holds the context of the request we are processing.
type admissionReviewRequest struct {
	log    *spanlogger.SpanLogger
	req    v1.AdmissionReview
	client k8sClient
}

// canIgnorePDB returns true for pod conditions that allow the pod to be deleted without checking PDBs.
// Adapted from https://github.com/kubernetes/kubernetes/blob/master/pkg/registry/core/pod/storage/eviction.go
func canIgnorePDB(pod *corev1.Pod) bool {
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed ||
		pod.Status.Phase == corev1.PodPending || !pod.DeletionTimestamp.IsZero() {
		return true
	}
	return false
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
func (r *admissionReviewRequest) denyWithReason(reason string, httpStatusCode int32) *v1.AdmissionResponse {
	rsp := v1.AdmissionResponse{
		Allowed: false,
		UID:     r.req.Request.UID,
	}
	rsp.Result = &metav1.Status{
		Message: reason,
		Code:    httpStatusCode,
	}
	return &rsp
}

// allowWithWarning constructs an allowed AdmissionResponse with the given warning included in the response warnings attribute.
// Per kubernetes -
// Don't include a "Warning:" prefix in the message
// Use warning messages to describe problems the client making the API request should correct or be aware of
// Limit warnings to 120 characters if possible
func (r *admissionReviewRequest) allowWithWarning(warning string) *v1.AdmissionResponse {
	rsp := v1.AdmissionResponse{
		Allowed: true,
		UID:     r.req.Request.UID,
	}
	rsp.Warnings = append(rsp.Warnings, warning)
	return &rsp
}

// allow constructs an allowed AdmissionResponse
func (r *admissionReviewRequest) allow() *v1.AdmissionResponse {
	rsp := v1.AdmissionResponse{
		Allowed: true,
		UID:     r.req.Request.UID,
	}
	return &rsp
}

// PodEviction is the entry point / handler for the pod-eviction endpoint request.
// Note - we do not perform anything different for dry-run requests (ar.Request.DryRun) as we do not modify any state in this webhook.
func PodEviction(ctx context.Context, l log.Logger, ar v1.AdmissionReview, kubeClient kubernetes.Interface, pdbCache *zpdb.ZpdbCache, podEvictionCache *zpdb.PodEvictionCache) *v1.AdmissionResponse {
	logger, ctx := spanlogger.New(ctx, l, "admission.PodEviction()", tenantResolver)
	defer logger.Finish()

	var pod *corev1.Pod
	var sts *appsv1.StatefulSet
	var err error

	request := admissionReviewRequest{req: ar, log: logger, client: k8sClient{ctx: ctx, kubeClient: kubeClient}}

	// set up key=value pairs we include on all logs within this context
	request.initLogger()

	// validate that this is a valid pod eviction create request
	if err = request.validate(); err != nil {
		level.Error(request.log).Log(logMsg, fmt.Sprintf("%s - not a valid create pod eviction request", logAllow), "err", err)
		return request.allowWithWarning(err.Error())
	}

	// get the pod which has been requested for eviction
	if pod, err = request.client.podByName(request.req.Request.Namespace, request.req.Request.Name); err != nil {
		level.Error(request.log).Log(logMsg, fmt.Sprintf("%s - unable to find pod by name", logAllow))
		return request.allowWithWarning(err.Error())
	}

	// evicting a terminal pod should result in direct deletion of pod as it already caused disruption by the time we are evicting.
	if canIgnorePDB(pod) {
		level.Info(request.log).Log(logMsg, fmt.Sprintf("%s - pod is not ready", logAllow))
		return request.allowWithWarning("pod is not ready")
	}

	pdbConfig, deny, err := pdbCache.Find(pod)
	if err != nil {
		if deny {
			level.Error(request.log).Log(logMsg, fmt.Sprintf("%s - %v", logDeny, err))
			return request.denyWithReason(err.Error(), httpStatusMisconfiguration)
		} else {
			level.Info(request.log).Log(logMsg, fmt.Sprintf("%s - %v", logAllow, err))
			return request.allowWithWarning(err.Error())
		}
	}

	// get the StatefulSet who manages this pod
	if sts, err = request.client.owner(pod); err != nil {
		level.Error(request.log).Log(logMsg, fmt.Sprintf("%s - unable to find pod owner", logAllow), "err", err)
		return request.allowWithWarning(err.Error())
	}

	// ie ingester-zone-a
	request.log.SetSpanAndLogTag("owner", sts.Name)

	lock := findLock(pdbConfig.Name())
	lock.Lock()
	defer lock.Unlock()

	// the number of allowed unavailable pods in other zones.
	maxUnavailable := pdbConfig.MaxUnavailablePods(sts)
	if maxUnavailable == 0 {
		level.Info(request.log).Log(logMsg, fmt.Sprintf("%s - max unavailable = 0", logDeny))
		return request.denyWithReason("max unavailable = 0", httpStatusPdbDisabled)
	}

	// Find the other StatefulSets which span all zones.
	// Assumption - each StatefulSet manages all the pods for a single zone. This list of StatefulSets covers all zones.
	// During a migration it may be possible to break this assumption, but during a migration the maxUnavailable will be set to 0 and the eviction request will be denied.
	otherStatefulSets, err := request.client.findRelatedStatefulSets(sts, pdbConfig.StsSelector())
	if err != nil || otherStatefulSets == nil || len(otherStatefulSets.Items) <= 1 {
		level.Error(request.log).Log(logMsg, "unable to find related stateful sets - continuing with single zone")
		otherStatefulSets = &appsv1.StatefulSetList{Items: []appsv1.StatefulSet{*sts}}
	}

	// this is the partition of the pod being evicted - for classic zones the partition will be an empty string
	partition, err := pdbConfig.PodPartition(pod)
	if err != nil {
		return request.denyWithReason(err.Error(), httpStatusMisconfiguration)
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
		// zone mode - each zone is individually evaluated against the zpdb
		pdb = makePdbValidatorZones(sts, len(otherStatefulSets.Items))
	}

	accumulatedResult := &podStatusResult{}
	for _, otherSts := range otherStatefulSets.Items {
		// test on whether we exclude the eviction pod's StatefulSet from the tally
		if !pdb.considerSts(&otherSts) {
			continue
		}

		result, err := request.client.podsNotRunningAndReady(&otherSts, pod, pdb.considerPod, podEvictionCache)
		if err != nil {
			level.Error(request.log).Log(logMsg, fmt.Sprintf("%s - unable to determine pod status in related StatefulSet", logDeny), "sts", otherSts.Name)
			return request.denyWithReason("unable to determine pod status in related StatefulSet", httpStatusInternalError)
		}

		accumulatedResult.tested += result.tested
		accumulatedResult.notReady += result.notReady
		accumulatedResult.unknown += result.unknown
	}

	if err = pdb.validate(accumulatedResult, maxUnavailable); err != nil {
		level.Error(request.log).Log(logMsg, fmt.Sprintf("%s - zpdb exceeded", logDeny), "err", err)
		return request.denyWithReason(err.Error(), httpStatusPdbExceeded)
	}

	// mark the pod as evicted
	// this entry will self expire and will be removed when a pod state change is observed
	podEvictionCache.Evict(pod)

	level.Info(request.log).Log(logMsg, fmt.Sprintf("%s - %s", logAllow, pdb.successMessage()))
	return request.allow()
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
			return fmt.Sprintf("zpdb met in single zone %s", sts.Name)
		},
		considerPod: partitionMatcher{
			same: func(pd *corev1.Pod) bool {
				return true
			},
		},
	}
	return &pdb
}

func makePdbValidatorPartition(sts *appsv1.StatefulSet, partition string, zones int, pdbConfig *zpdb.ZpdbConfig, log *spanlogger.SpanLogger) *pdbValidator {

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
			return fmt.Sprintf("zpdb met for partition %s across %d zones", partition, zones)
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
			return fmt.Sprintf("zpdb met across %d zones", zones)
		},
		considerPod: partitionMatcher{
			same: func(pd *corev1.Pod) bool {
				return true
			},
		},
	}
	return &pdb
}
