package zpdb

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type Metrics struct {
	ConfigurationsObserved *prometheus.CounterVec
	EvictionRequests       *prometheus.CounterVec
	InFlightRequests       *prometheus.GaugeVec
}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	return &Metrics{
		ConfigurationsObserved: promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
			Name: "rollout_operator_zpdb_configurations_observed_total",
			Help: "Number of zpdb configurations observed by the configuration controller.",
		}, []string{"result", "name"}),
		EvictionRequests: promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
			Name: "rollout_operator_zpdb_eviction_requests_total",
			Help: "Number of zpdb eviction requests.",
		}, []string{"reason", "pod", "status"}),
		InFlightRequests: promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
			Name: "rollout_operator_zpdb_inflight_eviction_requests_total",
			Help: "Number of zpdb eviction requests which are currently in-flight.",
		}, []string{"pod"}),
	}
}
