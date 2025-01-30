package middleware

import (
	"net/http"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/middleware"

	"github.com/grafana/rollout-operator/pkg/metrics"
)

func ClusterValidationRoundTripper(rt http.RoundTripper, namespace string, logger log.Logger, metrics *metrics.Metrics) http.RoundTripper {
	reporter := func(msg string, method string) {
		level.Warn(logger).Log("msg", msg, "method", method, "cluster_validation_label", namespace)
		metrics.InvalidClusterValidationLabels.WithLabelValues(method, "http", namespace).Inc()
	}
	return middleware.ClusterValidationRoundTripper(namespace, reporter, rt)
}
