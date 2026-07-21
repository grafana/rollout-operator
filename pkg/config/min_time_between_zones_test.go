package config

import (
	"bytes"
	"strings"
	"testing"

	"github.com/go-kit/log"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGetMinTimeBetweenZonesDownscale(t *testing.T) {
	t.Run("prefers annotation over label", func(t *testing.T) {
		var buf bytes.Buffer
		logger := log.NewLogfmtLogger(&buf)

		obj := &metav1.ObjectMeta{
			Name:      "sts-annotation-preferred",
			Namespace: "ns",
			Annotations: map[string]string{
				MinTimeBetweenZonesDownscaleAnnotationKey: "12h",
			},
			Labels: map[string]string{
				MinTimeBetweenZonesDownscaleLabelKey: "1h",
			},
		}

		value, ok := GetMinTimeBetweenZonesDownscale(obj, logger)
		require.True(t, ok)
		require.Equal(t, "12h", value)
		require.Empty(t, buf.String())
	})

	t.Run("falls back to label with warning", func(t *testing.T) {
		var buf bytes.Buffer
		logger := log.NewLogfmtLogger(&buf)

		obj := &metav1.ObjectMeta{
			Name:      "sts-label-fallback",
			Namespace: "ns",
			Labels: map[string]string{
				MinTimeBetweenZonesDownscaleLabelKey: "6h",
			},
		}

		value, ok := GetMinTimeBetweenZonesDownscale(obj, logger)
		require.True(t, ok)
		require.Equal(t, "6h", value)
		require.Contains(t, buf.String(), "is set as a label")
		require.Contains(t, buf.String(), "prefer the annotation")

		// Second read should not warn again for the same object.
		buf.Reset()
		value, ok = GetMinTimeBetweenZonesDownscale(obj, logger)
		require.True(t, ok)
		require.Equal(t, "6h", value)
		require.Empty(t, buf.String())
	})

	t.Run("empty annotation falls back to label", func(t *testing.T) {
		obj := &metav1.ObjectMeta{
			Name:      "sts-empty-annotation",
			Namespace: "ns",
			Annotations: map[string]string{
				MinTimeBetweenZonesDownscaleAnnotationKey: "",
			},
			Labels: map[string]string{
				MinTimeBetweenZonesDownscaleLabelKey: "3h",
			},
		}

		value, ok := GetMinTimeBetweenZonesDownscale(obj, log.NewNopLogger())
		require.True(t, ok)
		require.Equal(t, "3h", value)
	})

	t.Run("missing", func(t *testing.T) {
		value, ok := GetMinTimeBetweenZonesDownscale(&metav1.ObjectMeta{Name: "sts"}, log.NewNopLogger())
		require.False(t, ok)
		require.Empty(t, value)
	})
}

func TestMinTimeBetweenZonesDownscaleKeys(t *testing.T) {
	require.Equal(t, MinTimeBetweenZonesDownscaleAnnotationKey, MinTimeBetweenZonesDownscaleLabelKey)
	require.True(t, strings.HasPrefix(MinTimeBetweenZonesDownscaleAnnotationKey, "grafana.com/"))
}
