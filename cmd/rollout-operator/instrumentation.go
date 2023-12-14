package main

import (
	"net/http"

	"github.com/gorilla/mux"
	"github.com/grafana/dskit/instrument"
	"github.com/grafana/dskit/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type metrics struct {
	RequestDuration     *prometheus.HistogramVec
	ReceivedMessageSize *prometheus.HistogramVec
	SentMessageSize     *prometheus.HistogramVec
	InflightRequests    *prometheus.GaugeVec
}

func newMetrics(reg prometheus.Registerer) *metrics {
	return &metrics{
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
	}
}

func newInstrumentedRouter(metrics *metrics) (*mux.Router, http.Handler) {
	router := mux.NewRouter()

	httpMiddleware := []middleware.Interface{
		middleware.Instrument{
			RouteMatcher:     router,
			Duration:         metrics.RequestDuration,
			RequestBodySize:  metrics.ReceivedMessageSize,
			ResponseBodySize: metrics.SentMessageSize,
			InflightRequests: metrics.InflightRequests,
		},
	}

	return router, middleware.Merge(httpMiddleware...).Wrap(router)
}
