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

func TestRolloutHealthCheckValidatorHandlerSuccess(t *testing.T) {
	request := createRolloutHealthCheckAdmissionReviewValid()
	response := RolloutHealthCheckValidatingWebhookHandler(context.Background(), log.NewNopLogger(), request)
	require.NotNil(t, response.UID)
	require.True(t, response.Allowed)
}

func TestRolloutHealthCheckValidatorHandlerBadConfig(t *testing.T) {
	request := createRolloutHealthCheckAdmissionReviewInvalid()
	response := RolloutHealthCheckValidatingWebhookHandler(context.Background(), log.NewNopLogger(), request)
	require.NotNil(t, response.UID)
	require.False(t, response.Allowed)
	require.Contains(t, response.Result.Message, "prometheusURL")
	require.Equal(t, int32(http.StatusBadRequest), response.Result.Code)
}

func TestRolloutHealthCheckValidatorHandlerParseError(t *testing.T) {
	request := admissionv1.AdmissionReview{
		Request: &admissionv1.AdmissionRequest{
			UID:    "test-request-uid",
			Object: runtime.RawExtension{Raw: []byte(``)},
		},
	}
	response := RolloutHealthCheckValidatingWebhookHandler(context.Background(), log.NewNopLogger(), request)
	require.False(t, response.Allowed)
	require.Equal(t, int32(http.StatusBadRequest), response.Result.Code)
}

func createRolloutHealthCheckAdmissionReviewValid() admissionv1.AdmissionReview {
	return admissionv1.AdmissionReview{
		Request: &admissionv1.AdmissionRequest{
			UID: "test-request-uid",
			Object: runtime.RawExtension{
				Raw: []byte(`{
					"apiVersion": "rollout-operator.grafana.com/v1",
					"kind": "RolloutHealthCheck",
					"metadata": {
						"name": "ingester-cell-health",
						"namespace": "test"
					},
					"spec": {
						"selector": {
							"matchLabels": {
								"rollout-group": "ingester"
							}
						},
						"prometheusURL": "http://prometheus:9090",
						"checks": [
							{
								"name": "errors",
								"query": "scalar(sum(rate(errors{${targetMatchers}}[${range}])))",
								"successQuery": "(${current} < bool (${baseline}))"
							}
						]
					}
				}`),
			},
		},
	}
}

func createRolloutHealthCheckAdmissionReviewInvalid() admissionv1.AdmissionReview {
	return admissionv1.AdmissionReview{
		Request: &admissionv1.AdmissionRequest{
			UID: "test-request-uid",
			Object: runtime.RawExtension{
				Raw: []byte(`{
					"apiVersion": "rollout-operator.grafana.com/v1",
					"kind": "RolloutHealthCheck",
					"metadata": {
						"name": "ingester-cell-health",
						"namespace": "test"
					},
					"spec": {
						"selector": {
							"matchLabels": {
								"rollout-group": "ingester"
							}
						},
						"checks": [
							{
								"name": "errors",
								"query": "scalar(sum(rate(errors{${targetMatchers}}[${range}])))",
								"successQuery": "(${current} < bool (${baseline}))"
							}
						]
					}
				}`),
			},
		},
	}
}
