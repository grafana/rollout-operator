package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	admissionv1 "k8s.io/api/admission/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/grafana/rollout-operator/pkg/admission"
	"github.com/grafana/rollout-operator/pkg/status"
	"github.com/grafana/rollout-operator/pkg/tlscert"
	"github.com/grafana/rollout-operator/pkg/zpdb"
)

func TestAdmissionAvailableBeforeControllerSync(t *testing.T) {
	for _, resource := range []string{"zoneawarepoddisruptionbudgets", "pods", "statefulsets", "validatingwebhookconfigurations"} {
		t.Run(resource, func(t *testing.T) {
			cfg := newValidConfig(t)
			cfg.serverTLSEnabled = true
			cfg.leaderElectionEnabled = true
			cfg.kubePodName = "operator"
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			cfg.serverTLSPort = listener.Addr().(*net.TCPAddr).Port
			require.NoError(t, listener.Close())
			logger := log.NewNopLogger()
			cert, err := tlscert.NewSelfSignedCertProvider("test", []string{"localhost"}, nil, time.Hour).Certificate(t.Context())
			require.NoError(t, err)
			secret := &corev1.Secret{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}, Data: map[string][]byte{"ca": cert.CA, "cert": cert.Cert, "key": cert.Key}}
			blocked, published := atomic.NewBool(false), atomic.NewBool(false)
			certificateAttempts := atomic.NewInt32(0)
			caPatchAttempts := atomic.NewInt32(0)
			caPatched := atomic.NewBool(false)
			webhook := &admissionregistrationv1.ValidatingWebhookConfiguration{
				TypeMeta:   metav1.TypeMeta{APIVersion: "admissionregistration.k8s.io/v1", Kind: "ValidatingWebhookConfiguration"},
				ObjectMeta: metav1.ObjectMeta{Name: "test", Labels: map[string]string{"grafana.com/inject-rollout-operator-ca": "true", "grafana.com/namespace": cfg.kubeNamespace}},
				Webhooks:   []admissionregistrationv1.ValidatingWebhook{{Name: "test"}},
			}
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/validatingwebhookconfigurations/test") {
					if r.Method == http.MethodPatch {
						if caPatchAttempts.Inc() == 1 {
							http.Error(w, "CA publication unavailable", http.StatusServiceUnavailable)
							return
						}
						var patched admissionregistrationv1.ValidatingWebhookConfiguration
						if json.NewDecoder(r.Body).Decode(&patched) == nil && bytes.Equal(patched.Webhooks[0].ClientConfig.CABundle, cert.CA) {
							caPatched.Store(true)
						}
					}
					_ = json.NewEncoder(w).Encode(webhook)
					return
				}
				if strings.Contains(r.URL.Path, "/secrets/") {
					if resource == "zoneawarepoddisruptionbudgets" && certificateAttempts.Inc() == 1 {
						http.Error(w, "secret API unavailable", http.StatusServiceUnavailable)
						return
					}
					_ = json.NewEncoder(w).Encode(secret)
					return
				}
				if r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/pods/operator") {
					published.Store(true)
					_, _ = fmt.Fprint(w, `{"apiVersion":"v1","kind":"Pod"}`)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/"+resource) {
					blocked.Store(true)
					http.Error(w, "cache unavailable", http.StatusServiceUnavailable)
					return
				}
				if r.URL.Query().Get("watch") == "true" {
					http.Error(w, "watch unavailable", http.StatusServiceUnavailable)
					return
				}
				if resource == "pods" && strings.HasSuffix(r.URL.Path, "/validatingwebhookconfigurations") {
					_ = json.NewEncoder(w).Encode(&admissionregistrationv1.ValidatingWebhookConfigurationList{
						TypeMeta: metav1.TypeMeta{APIVersion: "admissionregistration.k8s.io/v1", Kind: "ValidatingWebhookConfigurationList"},
						ListMeta: metav1.ListMeta{ResourceVersion: "1"}, Items: []admissionregistrationv1.ValidatingWebhookConfiguration{*webhook},
					})
					return
				}
				kind, version := "ZoneAwarePodDisruptionBudgetList", "rollout-operator.grafana.com/v1"
				switch {
				case strings.HasSuffix(r.URL.Path, "/pods"):
					kind, version = "PodList", "v1"
				case strings.HasSuffix(r.URL.Path, "/statefulsets"):
					kind, version = "StatefulSetList", "apps/v1"
				case strings.HasSuffix(r.URL.Path, "/validatingwebhookconfigurations"):
					kind, version = "ValidatingWebhookConfigurationList", "admissionregistration.k8s.io/v1"
				case strings.HasSuffix(r.URL.Path, "/mutatingwebhookconfigurations"):
					kind, version = "MutatingWebhookConfigurationList", "admissionregistration.k8s.io/v1"
				}
				_, _ = fmt.Fprintf(w, `{"apiVersion":%q,"kind":%q,"metadata":{"resourceVersion":"1"},"items":[]}`, version, kind)
			}))
			defer api.Close()
			clientConfig := &rest.Config{Host: api.URL}
			client, err := kubernetes.NewForConfig(clientConfig)
			require.NoError(t, err)
			dynamicClient, err := dynamic.NewForConfig(clientConfig)
			require.NoError(t, err)
			reg := prometheus.NewRegistry()
			ready := atomic.NewBool(false)
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() {
				defer close(done)
				runOperator(ctx, cfg, func(string) *rest.Config { return clientConfig }, nil, logger, client, dynamicClient, nil, nil, nil, reg, newMetrics(reg), zpdb.NewMetrics(reg), ready, &status.Holder{})
			}()
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("startup did not stop on cancellation")
				}
				require.False(t, ready.Load())
			}()
			require.Eventually(t, func() bool { return ready.Load() && published.Load() && blocked.Load() }, 5*time.Second, 10*time.Millisecond)
			if resource == "pods" {
				require.Eventually(t, caPatched.Load, 5*time.Second, 10*time.Millisecond)
				require.GreaterOrEqual(t, caPatchAttempts.Load(), int32(2))
			}
			transport := &http.Transport{TLSClientConfig: tlsConfigForTest(t, cert.CA)}
			defer transport.CloseIdleConnections()
			httpClient := &http.Client{Transport: transport, Timeout: time.Second}
			send := func(path string, request *admissionv1.AdmissionRequest) *admissionv1.AdmissionResponse {
				t.Helper()
				body, err := json.Marshal(admissionv1.AdmissionReview{TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"}, Request: request})
				require.NoError(t, err)
				response, err := httpClient.Post(fmt.Sprintf("https://localhost:%d%s", cfg.serverTLSPort, path), "application/json", bytes.NewReader(body))
				require.NoError(t, err)
				defer response.Body.Close()
				require.Equal(t, http.StatusOK, response.StatusCode)
				var review admissionv1.AdmissionReview
				require.NoError(t, json.NewDecoder(response.Body).Decode(&review))
				require.NotNil(t, review.Response)
				return review.Response
			}
			for _, maxUnavailable := range []int{1, -1} {
				response := send(admission.ZpdbValidatorWebhookPath, &admissionv1.AdmissionRequest{UID: "validation", Object: runtime.RawExtension{Raw: []byte(fmt.Sprintf(`{"apiVersion":"rollout-operator.grafana.com/v1","kind":"ZoneAwarePodDisruptionBudget","metadata":{"name":"test"},"spec":{"maxUnavailable":%d,"selector":{"matchLabels":{"rollout-group":"test"}}}}`, maxUnavailable))}})
				require.Equal(t, maxUnavailable >= 0, response.Allowed, "%+v", response.Result)
			}
			if resource == "zoneawarepoddisruptionbudgets" || resource == "pods" {
				response := send(zpdb.PodEvictionWebhookPath, &admissionv1.AdmissionRequest{UID: "eviction", Name: "pod", Namespace: cfg.kubeNamespace, Operation: admissionv1.Create, Kind: metav1.GroupVersionKind{Group: "policy", Version: "v1", Kind: "Eviction"}, Resource: metav1.GroupVersionResource{Group: "policy", Version: "v1", Resource: "pods"}, SubResource: "eviction", Object: runtime.RawExtension{Raw: []byte(`{"apiVersion":"policy/v1","kind":"Eviction","metadata":{"name":"pod","namespace":"test"}}`)}})
				require.False(t, response.Allowed)
				require.EqualValues(t, http.StatusServiceUnavailable, response.Result.Code)
			}
		})
	}
}

func TestRetryInitialization(t *testing.T) {
	t.Run("transient error", func(t *testing.T) {
		calls := 0
		require.NoError(t, retryInitialization(t.Context(), log.NewNopLogger(), "test", func() error {
			calls++
			if calls == 1 {
				return fmt.Errorf("wrapped: %w", apierrors.NewServiceUnavailable("not ready"))
			}
			return nil
		}))
		require.Equal(t, 2, calls)
	})
	t.Run("permanent error", func(t *testing.T) {
		expected := errors.New("invalid certificate")
		require.ErrorIs(t, retryInitialization(t.Context(), log.NewNopLogger(), "test", func() error { return expected }), expected)
	})
	t.Run("cancel while retrying", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		require.ErrorIs(t, retryInitialization(ctx, log.NewNopLogger(), "test", func() error {
			cancel()
			return apierrors.NewServiceUnavailable("not ready")
		}), context.Canceled)
	})
}

func tlsConfigForTest(t *testing.T, ca []byte) *tls.Config {
	t.Helper()
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(ca))
	return &tls.Config{RootCAs: pool}
}
