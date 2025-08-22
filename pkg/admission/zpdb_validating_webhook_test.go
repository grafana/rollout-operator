package admission

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-kit/log"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// TestZoneAwarePdbValidatorHandlerSuccess tests with a valid configuration
func TestZoneAwarePdbValidatorHandlerSuccess(t *testing.T) {
	request := createValidatingWebHookAdmissionReviewValid()
	assertAllowResponse(t, request)
}

// TestZoneAwarePdbValidatorHandlerBadConfig tests with an invalid configuration
// See other test files for in-depth config validation
func TestZoneAwarePdbValidatorHandlerBadConfig(t *testing.T) {
	request := createValidatingWebHookAdmissionReviewInvalid()
	assertDenyResponse(t, request, "invalid value: max unavailable must be 0 <= val, got -1", http.StatusBadRequest)
}

// TestZoneAwarePdbValidatorHandlerParseError tests with a structural error in parsing the request object
func TestZoneAwarePdbValidatorHandlerParseError(t *testing.T) {
	request := createValidatingWebHookAdmissionReviewNoObject()
	assertDenyResponse(t, request, "unexpected end of JSON input", http.StatusBadRequest)
}

func assertDenyResponse(t *testing.T, request admissionv1.AdmissionReview, reason string, statusCode int) {
	response := ZoneAwarePdbValidatingWebhookHandler(context.Background(), log.NewNopLogger(), request)
	require.NotNil(t, response.UID)
	require.False(t, response.Allowed)
	require.Equal(t, reason, response.Result.Message)
	require.Equal(t, int32(statusCode), response.Result.Code)
}

func assertAllowResponse(t *testing.T, request admissionv1.AdmissionReview) {
	response := ZoneAwarePdbValidatingWebhookHandler(context.Background(), log.NewNopLogger(), request)
	require.NotNil(t, response.UID)
	require.True(t, response.Allowed)
}

func createValidatingWebHookAdmissionReviewValid() admissionv1.AdmissionReview {
	return admissionv1.AdmissionReview{
		Request: &admissionv1.AdmissionRequest{
			UID: "test-request-uid",
			Object: runtime.RawExtension{
				Raw: []byte(`{
					"apiVersion": "rollout-operator.grafana.com/v1",
					"kind": "ZoneAwarePodDisruptionBudget",
					"metadata": {
						"name": "test",
						"namespace": "tesst"
					},
					"spec": {
						"maxUnavailable": 1,
						"selector": {
							"matchLabels": {
								"rollout-group": "test-app"
							}
						}
					}
				}`),
			},
		},
	}
}

func createValidatingWebHookAdmissionReviewInvalid() admissionv1.AdmissionReview {
	return admissionv1.AdmissionReview{
		Request: &admissionv1.AdmissionRequest{
			UID: "test-request-uid",
			Object: runtime.RawExtension{
				Raw: []byte(`{
					"apiVersion": "rollout-operator.grafana.com/v1",
					"kind": "ZoneAwarePodDisruptionBudget",
					"metadata": {
						"name": "test",
						"namespace": "test"
					},
					"spec": {
						"maxUnavailable": -1,
						"selector": {
							"matchLabels": {
								"rollout-group": "test-app"
							}
						}
					}
				}`),
			},
		},
	}
}

func createValidatingWebHookAdmissionReviewNoObject() admissionv1.AdmissionReview {
	return admissionv1.AdmissionReview{
		Request: &admissionv1.AdmissionRequest{
			UID: "test-request-uid",
		},
	}
}
