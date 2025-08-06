package admission

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type zoneAwarePdbTestContext struct {
	ctx     context.Context
	request admissionv1.AdmissionReview
	logs    *dummyLogger
}

// TestZoneAwarePdbValidatorHandlerSuccess tests with a valid configuration
func TestZoneAwarePdbValidatorHandlerSuccess(t *testing.T) {
	test := newZoneAwarePdbTestContext(createValidatingWebHookAdmissionReviewValid())
	test.assertAllowResponse(t)
}

// TestZoneAwarePdbValidatorHandlerBadConfig tests with an invalid configuration
// See other test files for in-depth config validation
func TestZoneAwarePdbValidatorHandlerBadConfig(t *testing.T) {
	test := newZoneAwarePdbTestContext(createValidatingWebHookAdmissionReviewInvalid())
	test.assertDenyResponse(t, "invalid value - max unavailable must be 0 <= val - -1", 400)
}

// TestZoneAwarePdbValidatorHandlerParseError tests with a structural error in parsing the request object
func TestZoneAwarePdbValidatorHandlerParseError(t *testing.T) {
	test := newZoneAwarePdbTestContext(createValidatingWebHookAdmissionReviewNoObject())
	test.assertDenyResponse(t, "unexpected end of JSON input", 404)
}

func newZoneAwarePdbTestContext(request admissionv1.AdmissionReview) *zoneAwarePdbTestContext {
	testCtx := &zoneAwarePdbTestContext{}
	testCtx.ctx = context.Background()
	testCtx.request = request
	testCtx.logs = newDummyLogger()
	return testCtx
}

func (c *zoneAwarePdbTestContext) assertDenyResponse(t *testing.T, reason string, statusCode int) {
	response := ZoneAwarePdbValidatorHandler(context.Background(), c.logs, c.request)
	require.NotNil(t, response.UID)
	require.False(t, response.Allowed)
	require.Equal(t, reason, response.Result.Message)
	require.Equal(t, int32(statusCode), response.Result.Code)
}

func (c *zoneAwarePdbTestContext) assertAllowResponse(t *testing.T) {
	response := ZoneAwarePdbValidatorHandler(context.Background(), c.logs, c.request)
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
