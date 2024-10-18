// SPDX-License-Identifier: Apache-2.0
// Provenance-includes-location: https://github.com/kubernetes/kubernetes/blob/5539a5b80fe45cc04df4f53bd1047cd6d998bcf1/test/images/agnhost/webhook/main.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: The Kubernetes Authors.

package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	v1 "k8s.io/api/admission/v1"
	"k8s.io/api/admission/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// admitv1beta1Func handles a v1beta1 admission
type admitv1beta1Func func(context.Context, log.Logger, v1beta1.AdmissionReview, *kubernetes.Clientset) *v1beta1.AdmissionResponse

// AdmitV1Func handles a v1 admission
type AdmitV1Func func(context.Context, log.Logger, v1.AdmissionReview, *kubernetes.Clientset) *v1.AdmissionResponse

func delegateV1beta1AdmitToV1(f AdmitV1Func) admitv1beta1Func {
	return func(ctx context.Context, logger log.Logger, review v1beta1.AdmissionReview, api *kubernetes.Clientset) *v1beta1.AdmissionResponse {
		in := v1.AdmissionReview{Request: convertAdmissionRequestToV1(review.Request)}
		out := f(ctx, logger, in, api)
		return convertAdmissionResponseToV1beta1(out)
	}
}

// Serve handles the http portion of a request prior to handing to a V1Handler.
func Serve(admit AdmitV1Func, logger log.Logger, api *kubernetes.Clientset) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			if data, err := io.ReadAll(r.Body); err == nil {
				body = data
			}
		}

		// verify the content type is accurate
		contentType := r.Header.Get("Content-Type")
		if contentType != "application/json" {
			level.Error(logger).Log("msg", "wrong content type", "contentType", contentType, "expected", "application/json")
			http.Error(w, "wrong content type", http.StatusBadRequest)
			return
		}

		deserializer := codecs.UniversalDeserializer()
		obj, gvk, err := deserializer.Decode(body, nil, nil)
		if err != nil {
			msg := fmt.Sprintf("Request could not be decoded: %v", err)
			level.Error(logger).Log("msg", "request could not be decoded", "err", err)
			http.Error(w, msg, http.StatusBadRequest)
			return
		}

		var requestUid types.UID

		var responseObj runtime.Object
		switch *gvk {
		case v1beta1.SchemeGroupVersion.WithKind("AdmissionReview"):
			requestedAdmissionReview, ok := obj.(*v1beta1.AdmissionReview)
			if !ok {
				level.Error(logger).Log("msg", "unexpected type", "type", fmt.Sprintf("%T", obj), "expected", "*v1beta1.AdmissionReview")
				return
			}
			level.Debug(logger).Log(
				"msg", "handling request",
				"kind", requestedAdmissionReview.Request.Kind,
				"namespace", requestedAdmissionReview.Request.Namespace,
				"name", requestedAdmissionReview.Request.Name,
				"request.uid", requestedAdmissionReview.Request.UID,
				"request.username", requestedAdmissionReview.Request.UserInfo.Username,
			)
			responseAdmissionReview := &v1beta1.AdmissionReview{}
			responseAdmissionReview.SetGroupVersionKind(*gvk)
			responseAdmissionReview.Response = delegateV1beta1AdmitToV1(admit)(r.Context(), logger, *requestedAdmissionReview, api)
			responseAdmissionReview.Response.UID = requestedAdmissionReview.Request.UID
			responseObj = responseAdmissionReview
			requestUid = requestedAdmissionReview.Request.UID
		case v1.SchemeGroupVersion.WithKind("AdmissionReview"):
			requestedAdmissionReview, ok := obj.(*v1.AdmissionReview)
			if !ok {
				level.Error(logger).Log("msg", "unexpected type", "type", fmt.Sprintf("%T", obj), "expected", "*v1.AdmissionReview")
				return
			}
			level.Debug(logger).Log(
				"msg", "handling request",
				"kind", requestedAdmissionReview.Request.Kind,
				"namespace", requestedAdmissionReview.Request.Namespace,
				"name", requestedAdmissionReview.Request.Name,
				"request.uid", requestedAdmissionReview.Request.UID,
				"request.username", requestedAdmissionReview.Request.UserInfo.Username,
			)
			responseAdmissionReview := &v1.AdmissionReview{}
			responseAdmissionReview.SetGroupVersionKind(*gvk)
			responseAdmissionReview.Response = admit(r.Context(), logger, *requestedAdmissionReview, api)
			responseAdmissionReview.Response.UID = requestedAdmissionReview.Request.UID
			responseObj = responseAdmissionReview
			requestUid = requestedAdmissionReview.Request.UID
		default:
			msg := fmt.Sprintf("Unsupported group version kind: %v", gvk)
			level.Error(logger).Log("msg", "unsupported group version kind", "gvk", gvk)
			http.Error(w, msg, http.StatusBadRequest)
			return
		}

		level.Debug(logger).Log("msg", "sending response", "request.uid", requestUid, "response", responseObj)
		respBytes, err := json.Marshal(responseObj)
		if err != nil {
			level.Error(logger).Log("msg", "error marshaling response", "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(respBytes); err != nil {
			level.Error(logger).Log("msg", "error writing response", "err", err)
		}
	}
}
