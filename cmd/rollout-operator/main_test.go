package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"

	"github.com/grafana/rollout-operator/pkg/admission"
)

// newValidConfig returns a config populated with the flag defaults and the minimum required fields set,
// so that cfg.validate() passes. Individual tests override only the field under test.
func newValidConfig(t *testing.T) config {
	t.Helper()

	var cfg config
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg.register(fs)
	require.NoError(t, fs.Parse(nil))
	cfg.kubeNamespace = "test"
	return cfg
}

func TestConfigValidate(t *testing.T) {
	for name, tc := range map[string]struct {
		modify  func(*config) // mutates a baseline-valid config to set up the case
		wantErr string        // expected error substring; empty means the config must be valid
	}{
		"baseline is valid": {
			modify: func(*config) {},
		},
		"server-tls.request-timeout zero is rejected": {
			modify:  func(c *config) { c.serverTLSRequestTimeout = 0 },
			wantErr: "-server-tls.request-timeout must be positive",
		},
		"server-tls.request-timeout negative is rejected": {
			modify:  func(c *config) { c.serverTLSRequestTimeout = -time.Second },
			wantErr: "-server-tls.request-timeout must be positive",
		},
		"server-tls.request-timeout custom positive is valid": {
			modify: func(c *config) { c.serverTLSRequestTimeout = time.Minute },
		},
		"client-burst below 1 with qps>0 is rejected": {
			modify:  func(c *config) { c.kubeClientQPS = 5; c.kubeClientBurst = 0 },
			wantErr: "-kubernetes.client-burst must be at least 1",
		},
		"client-burst may be smaller than qps": {
			modify: func(c *config) { c.kubeClientQPS = 50; c.kubeClientBurst = 1 },
		},
		"qps<=0 disables limiting so burst is not validated": {
			modify: func(c *config) { c.kubeClientQPS = 0; c.kubeClientBurst = 0 },
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := newValidConfig(t)
			tc.modify(&cfg)

			err := cfg.validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestConfigDefaults(t *testing.T) {
	cfg := newValidConfig(t)
	require.Equal(t, 10*time.Second, cfg.serverTLSRequestTimeout)
	// Rate limiting is on by default (previously the 0/0 defaults disabled it); a regression flipping
	// these back to 0 would silently turn off client-side throttling, so assert them explicitly.
	require.Equal(t, float64(5), cfg.kubeClientQPS)
	require.Equal(t, 10, cfg.kubeClientBurst)
}

func TestDeprecatedZPDBReadyAnnotationPatchTimeoutFlag(t *testing.T) {
	for _, tc := range []struct {
		name        string
		args        []string
		wantWarning bool
	}{
		{name: "omitted", wantWarning: false},
		{name: "explicitly set", args: []string{"-" + deprecatedZPDBPodReadyAnnotationPatchTimeoutFlag + "=10s"}, wantWarning: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg config
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			cfg.register(fs)
			require.NoError(t, fs.Parse(tc.args))

			var logs bytes.Buffer
			warnDeprecatedFlags(fs, log.NewLogfmtLogger(&logs))
			if tc.wantWarning {
				require.Contains(t, logs.String(), "level=warn")
				require.Contains(t, logs.String(), "flag=-"+deprecatedZPDBPodReadyAnnotationPatchTimeoutFlag)
				require.Contains(t, logs.String(), "msg=\"deprecated flag has no effect\"")
				return
			}
			require.Empty(t, logs.String())
		})
	}
}

func TestAdmissionServedWhileControllersInitialize(t *testing.T) {
	ready := atomic.NewBool(false)
	mux := http.NewServeMux()
	mux.Handle("/ready", readyHandler(ready))
	mux.Handle(admission.ZpdbValidatorWebhookPath, admission.Serve(func(ctx context.Context, logger log.Logger, ar admissionv1.AdmissionReview, _ *kubernetes.Clientset) *admissionv1.AdmissionResponse {
		return admission.ZoneAwarePdbValidatingWebhookHandler(ctx, logger, ar)
	}, log.NewNopLogger(), nil, time.Second))

	initializing := make(chan struct{})
	resume := make(chan struct{})
	done := make(chan error, 1)
	var server *httptest.Server
	go func() {
		done <- startAdmissionAndControllers(true, ready, func() {
			server = httptest.NewServer(mux)
		}, func() error {
			close(initializing)
			<-resume
			return nil
		}, func() error { return nil })
	}()
	<-initializing
	t.Cleanup(func() {
		close(resume)
		require.NoError(t, <-done)
		server.Close()
	})

	response, err := server.Client().Get(server.URL + "/ready")
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)

	for _, tc := range []struct {
		name           string
		maxUnavailable int
		allowed        bool
	}{
		{name: "valid", maxUnavailable: 1, allowed: true},
		{name: "invalid", maxUnavailable: -1, allowed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj, err := json.Marshal(map[string]interface{}{
				"apiVersion": "rollout-operator.grafana.com/v1",
				"kind":       "ZoneAwarePodDisruptionBudget",
				"spec": map[string]interface{}{
					"maxUnavailable": tc.maxUnavailable,
					"selector":       map[string]interface{}{"matchLabels": map[string]string{"rollout-group": "ingester"}},
				},
			})
			require.NoError(t, err)
			body, err := json.Marshal(admissionv1.AdmissionReview{
				TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"},
				Request:  &admissionv1.AdmissionRequest{UID: "bootstrap", Namespace: "test", Object: runtime.RawExtension{Raw: obj}},
			})
			require.NoError(t, err)
			response, err := server.Client().Post(server.URL+admission.ZpdbValidatorWebhookPath, "application/json", bytes.NewReader(body))
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, http.StatusOK, response.StatusCode)
			var review admissionv1.AdmissionReview
			require.NoError(t, json.NewDecoder(response.Body).Decode(&review))
			require.NotNil(t, review.Response)
			require.Equal(t, tc.allowed, review.Response.Allowed)
			require.Equal(t, "bootstrap", string(review.Response.UID))
		})
	}
}

func TestStartAdmissionAndControllersReadiness(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			for _, failure := range []string{"", "eviction", "rollout"} {
				t.Run(failure, func(t *testing.T) {
					ready := atomic.NewBool(false)
					admissionStarted := false
					err := startAdmissionAndControllers(enabled, ready, func() {
						admissionStarted = true
					}, func() error {
						require.True(t, admissionStarted)
						require.Equal(t, enabled, ready.Load())
						if failure == "eviction" {
							return errors.New("eviction failed")
						}
						return nil
					}, func() error {
						require.Equal(t, enabled, ready.Load())
						if failure == "rollout" {
							return errors.New("rollout failed")
						}
						return nil
					})
					if failure == "" {
						require.NoError(t, err)
					} else {
						require.Error(t, err)
					}
					require.Equal(t, failure == "", ready.Load())
				})
			}
		})
	}
}
