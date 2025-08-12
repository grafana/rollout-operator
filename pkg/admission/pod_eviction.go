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

	errUnableToFindPodByName      = "unable to find pod by name"
	errPodNotReady                = "pod is not ready"
	errNoPdbForPod                = "no zone pod disruption budgets found for pod"
	errMaxUnavailableIs0          = "max unavailable = 0"
	errUnableToDetermineStsStatus = "unable to determine pod status in related StatefulSet"

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

// a lock used to control finding a specific PDB lock
var lock = sync.RWMutex{}

// a map of zpdb config name to a lock
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
func PodEviction(ctx context.Context, l log.Logger, ar v1.AdmissionReview, kubeClient kubernetes.Interface, pdbCache *zpdb.Cache, podEvictionCache *zpdb.PodEvictionCache) *v1.AdmissionResponse {
	logger, _ := spanlogger.New(ctx, l, "admission.PodEviction()", tenantResolver)
	defer logger.Finish()

	var pod *corev1.Pod
	var sts *appsv1.StatefulSet
	var err error

	request := admissionReviewRequest{req: ar, log: logger, client: k8sClient{ctx: ctx, kubeClient: kubeClient}}

	// set up key=value pairs we include on all logs within this context
	request.initLogger()

	level.Info(logger).Log("msg", "considering pod eviction")

	// validate that this is a valid pod eviction create request
	if err = request.validate(); err != nil {
		level.Error(request.log).Log(logMsg, fmt.Sprintf("%s - not a valid create pod eviction request", logAllow), "err", err)
		return request.allowWithWarning(err.Error())
	}

	// get the pod which has been requested for eviction
	if pod, err = request.client.podByName(request.req.Request.Namespace, request.req.Request.Name); err != nil {
		level.Error(request.log).Log(logMsg, fmt.Sprintf("%s - %s", logAllow, errUnableToFindPodByName))
		return request.allowWithWarning(err.Error())
	} else if pod == nil {
		level.Error(request.log).Log(logMsg, fmt.Sprintf("%s - %s", logAllow, errUnableToFindPodByName))
		return request.allowWithWarning(errUnableToFindPodByName)
	}

	// evicting a terminal pod should result in direct deletion of pod as it already caused disruption by the time we are evicting.
	if canIgnorePDB(pod) {
		level.Info(request.log).Log(logMsg, fmt.Sprintf("%s - %s", logAllow, errPodNotReady))
		return request.allowWithWarning(errPodNotReady)
	}

	pdbConfig, err := pdbCache.Find(pod)
	if err != nil {
		level.Error(request.log).Log(logMsg, fmt.Sprintf("%s - %v", logDeny, err))
		return request.denyWithReason(err.Error(), httpStatusMisconfiguration)
	} else if pdbConfig == nil {
		level.Info(request.log).Log(logMsg, fmt.Sprintf("%s - %s", logAllow, errNoPdbForPod))
		return request.allowWithWarning(errNoPdbForPod)
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
		level.Info(request.log).Log(logMsg, fmt.Sprintf("%s - %s", logDeny, errMaxUnavailableIs0))
		return request.denyWithReason(errMaxUnavailableIs0, httpStatusPdbDisabled)
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

	var pdb zpdb.Validator

	if len(otherStatefulSets.Items) == 1 {
		// single zone mode - we include the pod being evicted into the calculation and the maxAvailable is applied to the single zone / StatefulSet
		pdb = zpdb.NewValidatorSingleZone(&otherStatefulSets.Items[0])
	} else if len(partition) > 0 {
		// partition mode - the pod tallies are applied to all pods in other zones which relate to this partition
		pdb = zpdb.NewValidatorPartitionAware(sts, partition, len(otherStatefulSets.Items), pdbConfig, request.log)
	} else {
		// zone mode - we allow the eviction if no other zone is disrupted and the max unavailable within the eviction zone is not exceeded
		pdb = zpdb.NewValidatorZoneAware(sts, len(otherStatefulSets.Items))
	}

	for _, otherSts := range otherStatefulSets.Items {
		// test on whether we exclude the eviction pod's StatefulSet from the assessment
		if !pdb.ConsiderSts(&otherSts) {
			continue
		}

		result, err := request.client.podsNotRunningAndReady(&otherSts, pod, pdb.ConsiderPod(), podEvictionCache)
		if err != nil {
			level.Error(request.log).Log(logMsg, fmt.Sprintf("%s - %s", logDeny, errUnableToDetermineStsStatus), "sts", otherSts.Name)
			return request.denyWithReason(errUnableToDetermineStsStatus, httpStatusInternalError)
		}

		if err = pdb.AccumulateResult(&otherSts, result); err != nil {
			// opportunity to fail fast
			level.Error(request.log).Log(logMsg, fmt.Sprintf("%s - %v", logDeny, err), "sts", otherSts.Name)
			return request.denyWithReason(err.Error(), httpStatusPdbExceeded)
		}

	}

	if err = pdb.Validate(maxUnavailable); err != nil {
		level.Error(request.log).Log(logMsg, fmt.Sprintf("%s - zpdb exceeded", logDeny), "err", err)
		return request.denyWithReason(err.Error(), httpStatusPdbExceeded)
	}

	// mark the pod as evicted
	// this entry will self expire and will be removed when a pod state change is observed
	podEvictionCache.RecordEviction(pod)

	level.Info(request.log).Log(logMsg, fmt.Sprintf("%s - %s", logAllow, pdb.SuccessMessage()))
	return request.allow()
}
