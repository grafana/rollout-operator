package main

import (
	"net/http"

	"github.com/go-kit/log"
	"github.com/gorilla/mux"
	"github.com/grafana/dskit/instrument"
	"github.com/grafana/dskit/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type metrics struct {
	RequestDuration                             *prometheus.HistogramVec
	ReceivedMessageSize                         *prometheus.HistogramVec
	SentMessageSize                             *prometheus.HistogramVec
	InflightRequests                            *prometheus.GaugeVec
	ClientInvalidClusterValidationLabelRequests *prometheus.CounterVec
	ServerInvalidClusterValidaionLabelRequests  *prometheus.CounterVec
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
		ClientInvalidClusterValidationLabelRequests: promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
			Name: "rollout_operator_client_invalid_cluster_validation_label_requests_total",
			Help: "Number of requests with invalid cluster validation label.",
		}, []string{"method", "protocol", "request_cluster"}),
		ServerInvalidClusterValidaionLabelRequests: middleware.NewInvalidClusterRequests(reg, "rollout_operator"),
	}
}

func newInstrumentedRouter(metrics *metrics, cfg config, logger log.Logger) (*mux.Router, http.Handler) {
	router := mux.NewRouter()

	httpMiddleware := []middleware.Interface{
		middleware.RouteInjector{
			RouteMatcher: router,
		},
		middleware.Tracer{},
		middleware.Instrument{
			Duration:         metrics.RequestDuration,
			RequestBodySize:  metrics.ReceivedMessageSize,
			ResponseBodySize: metrics.SentMessageSize,
			InflightRequests: metrics.InflightRequests,
		},
	}
	if cfg.clusterValidationCfg.Enabled {
		// HTTP server side cluster validation.
		httpMiddleware = append(httpMiddleware, middleware.ClusterValidationMiddleware(
			[]string{cfg.kubeNamespace},
			cfg.clusterValidationCfg,
			metrics.ServerInvalidClusterValidaionLabelRequests,
			logger,
		))
	}

	return router, middleware.Merge(httpMiddleware...).Wrap(router)
}
