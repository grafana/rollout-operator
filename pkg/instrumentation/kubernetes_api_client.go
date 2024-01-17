package instrumentation

import (
	"net/http"
	"strconv"
	"time"

	"github.com/grafana/dskit/instrument"
	"github.com/opentracing-contrib/go-stdlib/nethttp"
	"github.com/opentracing/opentracing-go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"k8s.io/client-go/rest"
)

type kubernetesAPIClientInstrumentation struct {
	next http.RoundTripper
	hist *prometheus.HistogramVec
}

func InstrumentKubernetesAPIClient(cfg *rest.Config, reg prometheus.Registerer) {
	hist := promauto.With(reg).NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "rollout_operator_kubernetes_api_client_request_duration_seconds",
			Help:    "Time (in seconds) spent waiting for requests to the Kubernetes API",
			Buckets: instrument.DefBuckets,
		},
		[]string{"path", "method", "status_code"},
	)

	cfg.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return &kubernetesAPIClientInstrumentation{
			next: &nethttp.Transport{RoundTripper: rt},
			hist: hist,
		}
	})
}

func (k *kubernetesAPIClientInstrumentation) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()

	req, ht := nethttp.TraceRequest(opentracing.GlobalTracer(), req)
	defer ht.Finish()

	resp, err := k.next.RoundTrip(req)
	duration := time.Since(start)
	instrument.ObserveWithExemplar(req.Context(), k.hist.WithLabelValues(req.URL.EscapedPath(), req.Method, strconv.Itoa(resp.StatusCode)), duration.Seconds())

	return resp, err
}
