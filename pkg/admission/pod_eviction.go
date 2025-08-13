// Provenance-includes-location: https://github.com/kubernetes/kubernetes
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: The Kubernetes Authors.

package admission

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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

// canIgnorePDBForPod returns true for pod conditions that allow the pod to be deleted without checking PDBs.
// Adapted from https://github.com/kubernetes/kubernetes/blob/master/pkg/registry/core/pod/storage/eviction.go
func (r *admissionReviewRequest) canIgnorePDBForPod(pod *corev1.Pod) bool {
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
		return fmt.Errorf("request operation is not create, got: %s", r.req.Request.Operation)
	}

	// ignore create operations other than subresource eviction
	if r.req.Request.SubResource != "eviction" {
		return fmt.Errorf("request SubResource is not eviction, got: %s", r.req.Request.SubResource)
	}

	// we won't be able to determine the eviction pod
	if (r.req.Request == nil || len(r.req.Request.Namespace) == 0) || len(r.req.Request.Name) == 0 {
		return errors.New("request did not include both a namespace and a name")
	}

	return nil
}

// initLogger initialises the logger with context of this request.
func (r *admissionReviewRequest) initLogger() {
	// this is the name of the pod
	r.log.SetSpanAndLogTag("object.name", r.req.Request.Name)
	r.log.SetSpanAndLogTag("object.resource", r.req.Request.Resource.Resource)
	r.log.SetSpanAndLogTag("object.namespace", r.req.Request.Namespace)
	// note that this is the UID of the request, it is not the UID of the pod
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
		Allowed:  true,
		UID:      r.req.Request.UID,
		Warnings: []string{warning},
	}
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

func (r *admissionReviewRequest) allowMessage(msg string) string {
	return fmt.Sprintf("pod eviction allowed - %s", msg)
}

func (r *admissionReviewRequest) denyMessage(msg string) string {
	return fmt.Sprintf("pod eviction denied - %s", msg)
}

// PodEviction is the entry point / handler for the pod-eviction endpoint request.
// Note - we do not perform anything different for dry-run requests (ar.Request.DryRun) as we do not modify any state in this webhook.
func PodEviction(ctx context.Context, l log.Logger, ar v1.AdmissionReview, kubeClient kubernetes.Interface, pdbCache *zpdb.Cache, podEvictionCache *zpdb.PodEvictionCache) *v1.AdmissionResponse {
	logger, _ := spanlogger.New(ctx, l, "admission.PodEviction()", tenantResolver)
	defer logger.Finish()

	request := admissionReviewRequest{req: ar, log: logger, client: k8sClient{ctx: ctx, kubeClient: kubeClient}}

	// set up key=value pairs we include on all logs within this context
	request.initLogger()

	level.Info(logger).Log("msg", "considering pod eviction")

	// validate that this is a valid pod eviction create request
	if err := request.validate(); err != nil {
		level.Warn(request.log).Log("msg", request.allowMessage("not a valid create pod eviction request"), "err", err)
		return request.allowWithWarning(err.Error())
	}

	// get the pod which has been requested for eviction
	var pod *corev1.Pod
	var err error
	if pod, err = request.client.podByName(request.req.Request.Namespace, request.req.Request.Name); err != nil {
		level.Error(request.log).Log("msg", request.allowMessage("unable to find pod by name"), "err", err)
		return request.allowWithWarning(err.Error())
	}

	// evicting a terminal pod should result in direct deletion of pod as it already caused disruption by the time we are evicting.
	if request.canIgnorePDBForPod(pod) {
		level.Info(request.log).Log("msg", request.allowMessage("pod is not ready"))
		return request.allowWithWarning("pod is not ready")
	}

	// find the pdb which applies to this pod
	// for pods not managed by a zpdb we allow the request, it will fall through to any static PDB defined for this pod
	pdbConfig, err := pdbCache.Find(pod)
	if err != nil {
		level.Error(request.log).Log("msg", request.denyMessage(err.Error()))
		return request.denyWithReason(err.Error(), http.StatusBadRequest)
	} else if pdbConfig == nil {
		level.Debug(request.log).Log("msg", request.allowMessage("pod is not within a zpdb scope"))
		return request.allow()
	}

	// get the StatefulSet who manages this pod - allow the request if this fails
	var sts *appsv1.StatefulSet
	if sts, err = request.client.owner(pod); err != nil {
		level.Error(request.log).Log("msg", request.allowMessage("unable to find pod owner"), "err", err)
		return request.allowWithWarning(err.Error())
	}

	// ie ingester-zone-a
	request.log.SetSpanAndLogTag("owner", sts.Name)

	lock := findLock(pdbConfig.Name)
	lock.Lock()
	defer lock.Unlock()

	// the number of allowed unavailable pods in other zones.
	maxUnavailable := pdbConfig.MaxUnavailablePods(sts)
	if maxUnavailable == 0 {
		level.Info(request.log).Log("msg", request.denyMessage("max unavailable = 0"))
		return request.denyWithReason("max unavailable = 0", http.StatusForbidden)
	}

	// Find the all StatefulSets which span all zones.
	// Assumption - each StatefulSet manages all the pods for a single zone. This list of StatefulSets covers all zones.
	// During a migration it may be possible to break this assumption, but during a migration the maxUnavailable will be set to 0 and the eviction request will be denied.
	allStatefulSets, err := request.client.findRelatedStatefulSets(sts.Namespace, pdbConfig.Selector)
	if err != nil || allStatefulSets == nil || len(allStatefulSets.Items) <= 1 {
		level.Error(request.log).Log("msg", "unable to find related stateful sets - continuing with single zone")
		allStatefulSets = &appsv1.StatefulSetList{Items: []appsv1.StatefulSet{*sts}}
	}

	// this is the partition of the pod being evicted - for classic zones the partition will be an empty string
	partition, err := pdbConfig.PodPartition(pod)
	if err != nil {
		return request.denyWithReason(err.Error(), http.StatusBadRequest)
	}

	// include the partition we are considering
	if len(partition) > 0 {
		request.log.SetSpanAndLogTag("partition", partition)
	}

	// include the number of zones we will be considering
	request.log.SetSpanAndLogTag("zones", len(allStatefulSets.Items))

	var pdb zpdb.Validator

	if len(allStatefulSets.Items) == 1 {
		// single zone mode - we include the pod being evicted into the calculation and the maxAvailable is applied to the single zone / StatefulSet
		pdb = zpdb.NewValidatorSingleZone(&allStatefulSets.Items[0])
	} else if len(partition) > 0 {
		// partition mode - the pod tallies are applied to all pods in other zones which relate to this partition
		pdb = zpdb.NewValidatorPartitionAware(sts, partition, len(allStatefulSets.Items), pdbConfig, request.log)
	} else {
		// zone mode - we allow the eviction if no other zone is disrupted and the max unavailable within the eviction zone is not exceeded
		pdb = zpdb.NewValidatorZoneAware(sts, len(allStatefulSets.Items))
	}

	for _, otherSts := range allStatefulSets.Items {
		// test on whether we exclude the eviction pod's StatefulSet from the assessment
		if !pdb.ConsiderSts(&otherSts) {
			continue
		}

		result, err := request.client.podsNotRunningAndReady(&otherSts, pod, pdb.ConsiderPod(), podEvictionCache)
		if err != nil {
			level.Error(request.log).Log("msg", request.denyMessage("unable to determine pod status in related StatefulSet"), "sts", otherSts.Name)
			return request.denyWithReason("unable to determine pod status in related StatefulSet", http.StatusInternalServerError)
		}

		if err = pdb.AccumulateResult(&otherSts, result); err != nil {
			// opportunity to fail fast
			level.Error(request.log).Log("msg", request.denyMessage(err.Error()), "sts", otherSts.Name)
			return request.denyWithReason(err.Error(), http.StatusTooManyRequests)
		}

	}

	if err = pdb.Validate(maxUnavailable); err != nil {
		level.Error(request.log).Log("msg", request.denyMessage("zpdb exceeded"), "err", err)
		return request.denyWithReason(err.Error(), http.StatusTooManyRequests)
	}

	// mark the pod as evicted
	// this entry will self expire and will be removed when a pod state change is observed
	podEvictionCache.RecordEviction(pod)

	level.Info(request.log).Log("msg", request.allowMessage(pdb.SuccessMessage()))
	return request.allow()
}
