package zpdb

import (
	"errors"
	"fmt"

	"github.com/grafana/dskit/spanlogger"
	v1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A admissionReviewRequest holds the context of the request we are processing.
// It also has utility methods for validating the request and crafting the AdmissionResponse
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
		Result: &metav1.Status{
			Message: reason,
			Code:    httpStatusCode,
		},
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
