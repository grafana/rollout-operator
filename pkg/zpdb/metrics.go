package zpdb

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type Metrics struct {
	ConfigurationsObserved   *prometheus.CounterVec
	EvictionRequests         *prometheus.CounterVec
	InFlightRequests         *prometheus.GaugeVec
	PodInformerLastEventTime prometheus.Gauge
}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	return &Metrics{
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
		// The eviction webhook decides whether a pod may be evicted from the pod informer's cache, so a
		// watch which has silently stopped delivering updates means those decisions are made on stale data.
		// Periodic resyncs are excluded from this timestamp because they are served from the cache and keep
		// arriving even when the watch is dead, which would make a dead watch look healthy.
		//
		// What this therefore measures is the time since the informer last observed a pod change, which in a
		// namespace of any size is a close proxy for the watch being alive. A namespace genuinely idle for
		// long enough will look stale without being stale.
		PodInformerLastEventTime: promauto.With(reg).NewGauge(prometheus.GaugeOpts{
			Name: "rollout_operator_zpdb_pod_informer_last_event_timestamp_seconds",
			Help: "Timestamp of the last pod add, update or delete delivered by the pod informer's watch, excluding periodic resyncs.",
		}),
	}
}
