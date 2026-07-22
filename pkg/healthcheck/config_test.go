package healthcheck

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestParseAndValidate(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
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
		require.Equal(t, ActionPause, cfg.Checks[0].OnFailure)
		require.Equal(t, defaultCurrentRange, cfg.Checks[0].CurrentRange)
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

	t.Run("invalid onFailure", func(t *testing.T) {
		obj := mockHealthCheckUnstructured(map[string]interface{}{
			"selector": map[string]interface{}{
				"matchLabels": map[string]interface{}{"rollout-group": "ingester"},
			},
			"prometheusURL": "http://prometheus:9090",
			"checks": []interface{}{
				map[string]interface{}{
					"name":         "errors",
					"onFailure":    "Nope",
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
