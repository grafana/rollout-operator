package main

import (
	"flag"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
