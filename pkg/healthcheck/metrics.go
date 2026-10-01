package healthcheck

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics for RolloutHealthCheck observation and evaluation.
type Metrics struct {
	ConfigurationsObserved *prometheus.CounterVec
	EvaluationsTotal       *prometheus.CounterVec
	Blocked                *prometheus.GaugeVec
	MisconfiguredTotal     *prometheus.CounterVec
	Misconfigured          *prometheus.GaugeVec
	QueryDuration          *prometheus.HistogramVec
}

// NewMetrics registers health-check metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	return &Metrics{
		ConfigurationsObserved: promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
			Name: "rollout_operator_health_check_configurations_observed_total",
			Help: "Total number of RolloutHealthCheck configuration events observed by outcome.",
		}, []string{"outcome"}),
		EvaluationsTotal: promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
			Name: "rollout_operator_health_check_evaluations_total",
			Help: "Total number of health check evaluations by result.",
		}, []string{"rollout_group", "check", "result"}),
		Blocked: promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
			Name: "rollout_operator_health_check_blocked",
			Help: "Whether a rollout group is currently blocked by a health check (1) or not (0).",
		}, []string{"rollout_group"}),
		MisconfiguredTotal: promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
			Name: "rollout_operator_health_check_misconfigured_total",
			Help: "Total number of misconfigured health-check bindings observed for a rollout group.",
		}, []string{"rollout_group"}),
		Misconfigured: promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
			Name: "rollout_operator_health_check_misconfigured",
			Help: "Whether a rollout group currently has a misconfigured health-check binding (1) or not (0).",
		}, []string{"rollout_group"}),
		QueryDuration: promauto.With(reg).NewHistogramVec(prometheus.HistogramOpts{
			Name:    "rollout_operator_health_check_query_duration_seconds",
			Help:    "Duration of Prometheus queries issued for health checks.",
			Buckets: prometheus.DefBuckets,
		}, []string{"rollout_group", "check", "query_type"}),
	}
}

// DeleteGroup removes series labeled with the given rollout group.
func (m *Metrics) DeleteGroup(rolloutGroup string) {
	if m == nil {
		return
	}
	m.Blocked.DeleteLabelValues(rolloutGroup)
	m.Misconfigured.DeleteLabelValues(rolloutGroup)
	m.MisconfiguredTotal.DeleteLabelValues(rolloutGroup)
	m.EvaluationsTotal.DeletePartialMatch(prometheus.Labels{"rollout_group": rolloutGroup})
	m.QueryDuration.DeletePartialMatch(prometheus.Labels{"rollout_group": rolloutGroup})
}
