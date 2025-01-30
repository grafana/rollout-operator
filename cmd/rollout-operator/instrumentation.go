package main

import (
	"net/http"

	"github.com/go-kit/log"
	"github.com/gorilla/mux"
	"github.com/grafana/dskit/middleware"

	"github.com/grafana/rollout-operator/pkg/metrics"
)

func newInstrumentedRouter(metrics *metrics.Metrics, namespace string, cfg config, logger log.Logger) (*mux.Router, http.Handler) {
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
	if namespace != "" {
		// HTTP server side cluster validation.
		httpMiddleware = append(httpMiddleware, middleware.ClusterValidationMiddleware(
			namespace,
			cfg.namespaceValidationExcludePaths,
			cfg.softNamespaceValidation,
			logger,
		))
	}

	return router, middleware.Merge(httpMiddleware...).Wrap(router)
}
