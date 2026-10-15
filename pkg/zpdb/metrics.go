package zpdb

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type Metrics struct {
	readinessMu            sync.Mutex
	observerReady          bool
	controllersInitialized bool
	ConfigurationsObserved *prometheus.CounterVec
	EvictionRequests       *prometheus.CounterVec
	InFlightRequests       *prometheus.GaugeVec
	ConfigObserverReady    prometheus.Gauge
}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	metrics := &Metrics{
		ConfigurationsObserved: promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
			Name: "rollout_operator_zpdb_configurations_observed_total",
			Help: "Number of zpdb configurations observed by the configuration controller.",
		}, []string{"result"}),
		EvictionRequests: promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
			Name: "rollout_operator_zpdb_eviction_requests_total",
			Help: "Number of zpdb eviction requests.",
		}, []string{"reason", "status"}),
		InFlightRequests: promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
			Name: "rollout_operator_zpdb_inflight_eviction_requests",
			Help: "Number of zpdb eviction requests which are currently in-flight.",
		}, []string{}),
		ConfigObserverReady: promauto.With(reg).NewGauge(prometheus.GaugeOpts{
			Name: "rollout_operator_zpdb_config_observer_ready",
			Help: "Whether ZPDB configuration has synchronized and operator controllers have initialized.",
		}),
	}
	metrics.ConfigObserverReady.Set(0)
	return metrics
}

// SetControllersInitialized keeps observer readiness false while operator startup is blocked.
func (m *Metrics) SetControllersInitialized(initialized bool) {
	m.readinessMu.Lock()
	defer m.readinessMu.Unlock()
	m.controllersInitialized = initialized
	m.updateReadinessLocked()
}

func (m *Metrics) setConfigObserverReady(ready bool) {
	m.readinessMu.Lock()
	defer m.readinessMu.Unlock()
	m.observerReady = ready
	m.updateReadinessLocked()
}

func (m *Metrics) updateReadinessLocked() {
	if m.observerReady && m.controllersInitialized {
		m.ConfigObserverReady.Set(1)
	} else {
		m.ConfigObserverReady.Set(0)
	}
}
