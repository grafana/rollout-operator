package admission

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/spanlogger"
	v1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/grafana/rollout-operator/pkg/healthcheck"
)

const (
	RolloutHealthCheckValidatorWebhookPath = "/admission/rollout-health-check-validation"
)

type rolloutHealthCheckValidatingWebhook struct {
	ctx     context.Context
	logger  *spanlogger.SpanLogger
	request v1.AdmissionReview
}

func (v *rolloutHealthCheckValidatingWebhook) initLogger() {
	v.logger.SetSpanAndLogTag("object.name", v.request.Request.Name)
	v.logger.SetSpanAndLogTag("object.resource", v.request.Request.Resource.Resource)
	v.logger.SetSpanAndLogTag("object.namespace", v.request.Request.Namespace)
	v.logger.SetSpanAndLogTag("request.uid", v.request.Request.UID)

	if v.request.Request.DryRun != nil {
		v.logger.SetSpanAndLogTag("request.dry_run", v.request.Request.DryRun)
	}
}

func (v *rolloutHealthCheckValidatingWebhook) parse() (int32, error) {
	var obj unstructured.Unstructured
	if err := json.Unmarshal(v.request.Request.Object.Raw, &obj); err != nil {
		level.Info(v.logger).Log("msg", errors.New("failed to unmarshal object"), "err", err)
		return int32(http.StatusBadRequest), err
	}

	_, err := healthcheck.ParseAndValidate(&obj)
	if err != nil {
		level.Info(v.logger).Log("msg", errors.New("parsing failed"), "err", err)
		return int32(http.StatusBadRequest), err
	}

	return 0, nil
}

func (v *rolloutHealthCheckValidatingWebhook) allow() *v1.AdmissionResponse {
	return &v1.AdmissionResponse{
		Allowed: true,
		UID:     v.request.Request.UID,
	}
}

func (v *rolloutHealthCheckValidatingWebhook) deny(reason string, httpStatusCode int32) *v1.AdmissionResponse {
	return &v1.AdmissionResponse{
		Allowed: false,
		UID:     v.request.Request.UID,
		Result: &metav1.Status{
			Message: reason,
			Code:    httpStatusCode,
		},
	}
}

// RolloutHealthCheckValidatingWebhookHandler validates RolloutHealthCheck custom resources.
func RolloutHealthCheckValidatingWebhookHandler(ctx context.Context, l log.Logger, ar v1.AdmissionReview) *v1.AdmissionResponse {
	logger, ctx := spanlogger.New(ctx, l, "admission.RolloutHealthCheckValidatingWebhookHandler()", tenantResolver)
	defer logger.Finish()

	validator := &rolloutHealthCheckValidatingWebhook{
		ctx:     ctx,
		logger:  logger,
		request: ar,
	}

	validator.initLogger()

	if httpStatusCode, err := validator.parse(); err != nil {
		return validator.deny(err.Error(), httpStatusCode)
	}

	return validator.allow()
}
