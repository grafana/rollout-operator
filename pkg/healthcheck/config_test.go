package healthcheck

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestParseAndValidate(t *testing.T) {
	t.Run("valid defaults", func(t *testing.T) {
		obj := mockHealthCheckUnstructured(map[string]interface{}{
			"selector": map[string]interface{}{
				"matchLabels": map[string]interface{}{"rollout-group": "ingester"},
			},
			"prometheusURL": "http://prometheus:9090",
			"checks": []interface{}{
				map[string]interface{}{
					"name":         "errors",
					"query":        `scalar(sum(rate(errors{${targetMatchers}}[${range}])))`,
					"successQuery": `(${current} < bool 1) + (${current} < bool (2 * ${baseline})) > bool 0`,
				},
			},
		})
		cfg, err := ParseAndValidate(obj)
		require.NoError(t, err)
		require.Equal(t, "ingester-cell-health", cfg.Name)
		require.Equal(t, "http://prometheus:9090", cfg.PrometheusURL)
		require.Len(t, cfg.Checks, 1)
		require.Equal(t, ActionPause, cfg.Checks[0].FailurePolicy.ExceededAction)
		require.Equal(t, ActionPause, cfg.Checks[0].ErrorPolicy.ExhaustedAction)
		require.Equal(t, ActionPause, cfg.Checks[0].NoDataPolicy.ExhaustedAction)
		require.Equal(t, defaultCurrentRange, cfg.Checks[0].CurrentRange)
		require.Equal(t, defaultConsecutiveFailures, cfg.Checks[0].FailurePolicy.ConsecutiveFailures)
		require.Equal(t, defaultErrorRetryInterval, cfg.Checks[0].ErrorPolicy.RetryInterval)
		require.Equal(t, defaultNoDataRetryInterval, cfg.Checks[0].NoDataPolicy.RetryInterval)
	})

	t.Run("explicit policies", func(t *testing.T) {
		obj := mockHealthCheckUnstructured(map[string]interface{}{
			"selector": map[string]interface{}{
				"matchLabels": map[string]interface{}{"rollout-group": "ingester"},
			},
			"prometheusURL": "http://prometheus:9090",
			"checks": []interface{}{
				map[string]interface{}{
					"name": "errors",
					"errorPolicy": map[string]interface{}{
						"retryInterval":   "5s",
						"maxAttempts":     int64(2),
						"exhaustedAction": "Warn",
					},
					"failurePolicy": map[string]interface{}{
						"reevaluationInterval": "1m",
						"consecutiveFailures":  int64(2),
						"consecutiveSuccesses": int64(2),
						"exceededAction":       "Pause",
					},
					"noDataPolicy": map[string]interface{}{
						"retryInterval":   "15s",
						"maxAttempts":     int64(4),
						"exhaustedAction": "Disabled",
					},
					"query":        `scalar(sum(rate(errors{${targetMatchers}}[${range}])))`,
					"successQuery": `(${current} < bool (${baseline}))`,
				},
			},
		})
		cfg, err := ParseAndValidate(obj)
		require.NoError(t, err)
		c := cfg.Checks[0]
		require.Equal(t, 5*time.Second, c.ErrorPolicy.RetryInterval)
		require.Equal(t, 2, c.ErrorPolicy.MaxAttempts)
		require.Equal(t, ActionWarn, c.ErrorPolicy.ExhaustedAction)
		require.Equal(t, time.Minute, c.FailurePolicy.ReevaluationInterval)
		require.Equal(t, 2, c.FailurePolicy.ConsecutiveFailures)
		require.Equal(t, 2, c.FailurePolicy.ConsecutiveSuccesses)
		require.Equal(t, 15*time.Second, c.NoDataPolicy.RetryInterval)
		require.Equal(t, 4, c.NoDataPolicy.MaxAttempts)
		require.Equal(t, ActionDisabled, c.NoDataPolicy.ExhaustedAction)
	})

	t.Run("missing placeholders", func(t *testing.T) {
		obj := mockHealthCheckUnstructured(map[string]interface{}{
			"selector": map[string]interface{}{
				"matchLabels": map[string]interface{}{"rollout-group": "ingester"},
			},
			"prometheusURL": "http://prometheus:9090",
			"checks": []interface{}{
				map[string]interface{}{
					"name":         "errors",
					"query":        `scalar(1)`,
					"successQuery": `${current} < bool 1`,
				},
			},
		})
		_, err := ParseAndValidate(obj)
		require.Error(t, err)
		require.Contains(t, err.Error(), placeholderTargetMatchers)
	})

	t.Run("duplicate check names", func(t *testing.T) {
		obj := mockHealthCheckUnstructured(map[string]interface{}{
			"selector": map[string]interface{}{
				"matchLabels": map[string]interface{}{"rollout-group": "ingester"},
			},
			"prometheusURL": "http://prometheus:9090",
			"checks": []interface{}{
				map[string]interface{}{
					"name":         "errors",
					"query":        `scalar(sum(rate(errors{${targetMatchers}}[${range}])))`,
					"successQuery": `(${current} < bool (${baseline}))`,
				},
				map[string]interface{}{
					"name":         "errors",
					"query":        `scalar(sum(rate(errors{${targetMatchers}}[${range}])))`,
					"successQuery": `(${current} < bool (${baseline}))`,
				},
			},
		})
		_, err := ParseAndValidate(obj)
		require.Error(t, err)
		require.Contains(t, err.Error(), "duplicate")
	})

	t.Run("invalid exhaustedAction", func(t *testing.T) {
		obj := mockHealthCheckUnstructured(map[string]interface{}{
			"selector": map[string]interface{}{
				"matchLabels": map[string]interface{}{"rollout-group": "ingester"},
			},
			"prometheusURL": "http://prometheus:9090",
			"checks": []interface{}{
				map[string]interface{}{
					"name": "errors",
					"errorPolicy": map[string]interface{}{
						"exhaustedAction": "Nope",
					},
					"query":        `scalar(sum(rate(errors{${targetMatchers}}[${range}])))`,
					"successQuery": `(${current} < bool (${baseline}))`,
				},
			},
		})
		_, err := ParseAndValidate(obj)
		require.Error(t, err)
	})
}

func TestParseStartedAtAnnotation(t *testing.T) {
	require.True(t, ParseStartedAtAnnotation("", "rev").IsZero())
	require.True(t, ParseStartedAtAnnotation("other=2026-01-01T00:00:00Z", "rev").IsZero())
	ts := ParseStartedAtAnnotation("rev=2026-01-02T03:04:05Z", "rev")
	require.False(t, ts.IsZero())
	require.Equal(t, "rev=2026-01-02T03:04:05Z", FormatStartedAtAnnotation("rev", ts))
}

func mockHealthCheckUnstructured(spec map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": RolloutHealthChecksSpecGroup + "/" + RolloutHealthChecksVersion,
		"kind":       RolloutHealthCheckKind,
		"metadata": map[string]interface{}{
			"name":       "ingester-cell-health",
			"generation": int64(1),
		},
		"spec": spec,
	}}
}
