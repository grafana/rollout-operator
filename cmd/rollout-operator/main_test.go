package main

import (
	"bytes"
	"context"
	"flag"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestConfigValidateLeaderElection(t *testing.T) {
	tests := map[string]func(*config){
		"empty lease name": func(cfg *config) {
			cfg.leaderElectionLeaseName = ""
		},
		"subsecond lease duration": func(cfg *config) {
			cfg.leaderElectionLeaseDuration = 500 * time.Millisecond
			cfg.leaderElectionRenewDeadline = 400 * time.Millisecond
			cfg.leaderElectionRetryPeriod = 100 * time.Millisecond
		},
		"lease duration not greater than renew deadline": func(cfg *config) {
			cfg.leaderElectionLeaseDuration = cfg.leaderElectionRenewDeadline
		},
		"serialized lease duration not greater than renew deadline": func(cfg *config) {
			cfg.leaderElectionLeaseDuration = 1500 * time.Millisecond
			cfg.leaderElectionRenewDeadline = 1400 * time.Millisecond
			cfg.leaderElectionRetryPeriod = 100 * time.Millisecond
		},
		"renew deadline does not allow for retry jitter": func(cfg *config) {
			cfg.leaderElectionRenewDeadline = 110 * time.Millisecond
			cfg.leaderElectionRetryPeriod = 100 * time.Millisecond
		},
		"non-positive retry period": func(cfg *config) {
			cfg.leaderElectionRetryPeriod = 0
		},
	}

	for name, modify := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := newValidConfig(t)
			cfg.leaderElectionEnabled = true
			modify(&cfg)
			require.Error(t, cfg.validate())
		})
	}
}

func TestRunWithLeaderElectionAcquiresLease(t *testing.T) {
	cfg := newValidConfig(t)
	cfg.leaderElectionEnabled = true
	cfg.leaderElectionLeaseDuration = 2 * time.Second
	cfg.leaderElectionRenewDeadline = time.Second
	cfg.leaderElectionRetryPeriod = 100 * time.Millisecond

	cfg.kubePodName = "test-pod"
	client := fake.NewSimpleClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: cfg.kubePodName, Namespace: cfg.kubeNamespace}})
	ready := atomic.NewBool(false)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	result := make(chan error, 1)

	go func() {
		result <- runWithLeaderElection(ctx, client, cfg, "test-pod", log.NewNopLogger(), ready, func(leaderCtx context.Context) {
			ready.Store(true)
			close(started)
			<-leaderCtx.Done()
		})
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting to acquire leader election lease")
	}

	lease, err := client.CoordinationV1().Leases(cfg.kubeNamespace).Get(t.Context(), cfg.leaderElectionLeaseName, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, lease.Spec.HolderIdentity)
	require.Equal(t, "test-pod", *lease.Spec.HolderIdentity)

	cancel()
	require.NoError(t, <-result)
	require.False(t, ready.Load())
}

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
	require.False(t, cfg.watchReplicaTemplates)
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

func TestStandbyReadyWithoutWebhookRoutingAndTakeover(t *testing.T) {
	cfg := newValidConfig(t)
	cfg.leaderElectionEnabled = true
	cfg.kubePodName = "standby"
	cfg.leaderElectionLeaseDuration = 2 * time.Second
	cfg.leaderElectionRenewDeadline = time.Second
	cfg.leaderElectionRetryPeriod = 100 * time.Millisecond
	other := "other-pod"
	leaseDuration := int32(60)
	now := metav1.NewMicroTime(time.Now())
	client := fake.NewSimpleClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: cfg.kubePodName, Namespace: cfg.kubeNamespace, Labels: map[string]string{leaderLabel: "true", "name": "rollout-operator"}}},
		&coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: cfg.leaderElectionLeaseName, Namespace: cfg.kubeNamespace}, Spec: coordinationv1.LeaseSpec{HolderIdentity: &other, LeaseDurationSeconds: &leaseDuration, RenewTime: &now}},
	)
	ready := atomic.NewBool(false)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan struct{})
	initialize := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- runWithLeaderElection(ctx, client, cfg, "standby-identity", log.NewNopLogger(), ready, func(leaderCtx context.Context) {
			close(started)
			select {
			case <-initialize:
			case <-leaderCtx.Done():
				return
			}
			if err := setLeaderLabel(leaderCtx, client, cfg, true); err != nil {
				return
			}
			ready.Store(true)
			<-leaderCtx.Done()
		})
	}()
	require.Eventually(t, ready.Load, time.Second, 10*time.Millisecond)
	pod, err := client.CoreV1().Pods(cfg.kubeNamespace).Get(t.Context(), cfg.kubePodName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "false", pod.Labels[leaderLabel])
	require.Equal(t, "rollout-operator", pod.Labels["name"])
	select {
	case <-started:
		t.Fatal("standby started leader-only work")
	default:
	}

	require.NoError(t, client.CoordinationV1().Leases(cfg.kubeNamespace).Delete(t.Context(), cfg.leaderElectionLeaseName, metav1.DeleteOptions{}))
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("standby did not take over")
	}
	require.False(t, ready.Load(), "leader must initialize before receiving traffic")
	pod, err = client.CoreV1().Pods(cfg.kubeNamespace).Get(t.Context(), cfg.kubePodName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "false", pod.Labels[leaderLabel])
	close(initialize)
	require.Eventually(t, ready.Load, time.Second, 10*time.Millisecond)
	pod, err = client.CoreV1().Pods(cfg.kubeNamespace).Get(t.Context(), cfg.kubePodName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "true", pod.Labels[leaderLabel])
	cancel()
	require.NoError(t, <-result)
	require.False(t, ready.Load())
	pod, err = client.CoreV1().Pods(cfg.kubeNamespace).Get(t.Context(), cfg.kubePodName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "false", pod.Labels[leaderLabel])
}

func TestLeaderRoutingPatchFailureKeepsPodUnready(t *testing.T) {
	cfg := newValidConfig(t)
	cfg.leaderElectionEnabled = true
	cfg.kubePodName = "missing-pod"
	ready := atomic.NewBool(false)
	err := runWithLeaderElection(t.Context(), fake.NewSimpleClientset(), cfg, "test", log.NewNopLogger(), ready, func(context.Context) {
		t.Error("controller started without clearing its routing label")
	})
	require.ErrorContains(t, err, "failed to clear leader pod label")
	require.False(t, ready.Load())
}

func TestLeaderElectionDisabledByDefault(t *testing.T) {
	cfg := newValidConfig(t)
	require.False(t, cfg.leaderElectionEnabled)
	cfg.leaderElectionLeaseName = ""
	cfg.leaderElectionLeaseDuration = 0
	require.NoError(t, cfg.validate())
	client := fake.NewSimpleClientset()
	started := false
	ctx := t.Context()
	require.NoError(t, runWithLeaderElection(ctx, client, cfg, "", log.NewNopLogger(), atomic.NewBool(false), func(operatorCtx context.Context) {
		require.Equal(t, ctx, operatorCtx)
		started = true
	}))
	require.True(t, started)
	require.NoError(t, setLeaderLabel(ctx, client, cfg, true))
	require.NoError(t, setLeaderLabel(ctx, client, cfg, false))
	require.Empty(t, client.Actions(), "default startup must not require Pod patch or Lease access")
}
