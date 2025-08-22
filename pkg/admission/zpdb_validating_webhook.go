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

	"github.com/grafana/rollout-operator/pkg/zpdb"
)

const (
	ZpdbValidatorWebhookPath = "/admission/zpdb-validation"
)

type zpdbValidatingWebhook struct {
	ctx     context.Context
	logger  *spanlogger.SpanLogger
	request v1.AdmissionReview
}

func (v *zpdbValidatingWebhook) initLogger() {
	v.logger.SetSpanAndLogTag("object.name", v.request.Request.Name)
	v.logger.SetSpanAndLogTag("object.resource", v.request.Request.Resource.Resource)
	v.logger.SetSpanAndLogTag("object.namespace", v.request.Request.Namespace)
	v.logger.SetSpanAndLogTag("request.uid", v.request.Request.UID)

	if v.request.Request.DryRun != nil {
		v.logger.SetSpanAndLogTag("request.dry_run", v.request.Request.DryRun)
	}
}

// parse attempts to parse the raw object to a ZpdbConfig (ZoneAwarePodDisruptionBudget).
// returns an error and http status code if the parse or validation fails
func (v *zpdbValidatingWebhook) parse() (int32, error) {
	var obj unstructured.Unstructured
	if err := json.Unmarshal(v.request.Request.Object.Raw, &obj); err != nil {
		level.Info(v.logger).Log("msg", errors.New("failed to unmarshal object"), "err", err)
		return int32(http.StatusBadRequest), err
	}

	_, err := zpdb.ParseAndValidate(&obj)
	if err != nil {
		level.Info(v.logger).Log("msg", errors.New("parsing failed"), "err", err)
		return int32(http.StatusBadRequest), err
	}

	return 0, nil
}

func (v *zpdbValidatingWebhook) allow() *v1.AdmissionResponse {
	rsp := v1.AdmissionResponse{
		Allowed: true,
		UID:     v.request.Request.UID,
	}
	return &rsp
}

func (v *zpdbValidatingWebhook) deny(reason string, httpStatusCode int32) *v1.AdmissionResponse {
	rsp := v1.AdmissionResponse{
		Allowed: false,
		UID:     v.request.Request.UID,
		Result: &metav1.Status{
			Message: reason,
			Code:    httpStatusCode,
		},
	}
	return &rsp
}

// ZoneAwarePdbValidatingWebhookHandler is a handler for a validating webhook configuration.
// If attempts to parse and validate the given object as a ZoneAwarePodDisruptionBudget configuration.
func ZoneAwarePdbValidatingWebhookHandler(ctx context.Context, l log.Logger, ar v1.AdmissionReview) *v1.AdmissionResponse {
	logger, ctx := spanlogger.New(ctx, l, "admission.ZoneAwarePdbValidatorHandler()", tenantResolver)
	defer logger.Finish()

	validator := &zpdbValidatingWebhook{
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
