package zpdb

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestConfigObserverReadinessRequiresControllerInitialization(t *testing.T) {
	m := NewMetrics(prometheus.NewRegistry())
	m.setConfigObserverReady(true)
	require.Equal(t, float64(0), testutil.ToFloat64(m.ConfigObserverReady))
	m.SetControllersInitialized(true)
	require.Equal(t, float64(1), testutil.ToFloat64(m.ConfigObserverReady))
	m.setConfigObserverReady(false)
	require.Equal(t, float64(0), testutil.ToFloat64(m.ConfigObserverReady))
	m.setConfigObserverReady(true)
	require.Equal(t, float64(1), testutil.ToFloat64(m.ConfigObserverReady))
	m.SetControllersInitialized(false)
	m.setConfigObserverReady(true)
	require.Equal(t, float64(0), testutil.ToFloat64(m.ConfigObserverReady))
}
