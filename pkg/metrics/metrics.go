package metrics

import (
	"github.com/grafana/dskit/instrument"
	"github.com/grafana/dskit/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type Metrics struct {
	RequestDuration                *prometheus.HistogramVec
	ReceivedMessageSize            *prometheus.HistogramVec
	SentMessageSize                *prometheus.HistogramVec
	InflightRequests               *prometheus.GaugeVec
	InvalidClusterValidationLabels *prometheus.CounterVec
}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	return &Metrics{
		RequestDuration: promauto.With(reg).NewHistogramVec(prometheus.HistogramOpts{
			Name:    "rollout_operator_request_duration_seconds",
			Help:    "Time (in seconds) spent serving HTTP requests.",
			Buckets: instrument.DefBuckets,
		}, []string{"method", "route", "status_code", "ws"}),
		ReceivedMessageSize: promauto.With(reg).NewHistogramVec(prometheus.HistogramOpts{
			Name:    "rollout_operator_request_message_bytes",
			Help:    "Size (in bytes) of messages received in the request.",
			Buckets: middleware.BodySizeBuckets,
		}, []string{"method", "route"}),
		SentMessageSize: promauto.With(reg).NewHistogramVec(prometheus.HistogramOpts{
			Name:    "rollout_operator_response_message_bytes",
			Help:    "Size (in bytes) of messages sent in response.",
			Buckets: middleware.BodySizeBuckets,
		}, []string{"method", "route"}),
		InflightRequests: promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
			Name: "rollout_operator_inflight_requests",
			Help: "Current number of inflight requests.",
		}, []string{"method", "route"}),
		InvalidClusterValidationLabels: promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
			Name: "rollout_operator_request_invalid_cluster_validation_labels_total",
			Help: "Number of requests with invalid cluster validation label.",
		}, []string{"method", "protocol", "request_cluster"}),
	}
}
