package instrumentation

import (
	"fmt"
	"net/http"
	"regexp"
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
		return newInstrumentation(rt, hist)
	})
}

func newInstrumentation(rt http.RoundTripper, hist *prometheus.HistogramVec) *kubernetesAPIClientInstrumentation {
	return &kubernetesAPIClientInstrumentation{
		next: &nethttp.Transport{RoundTripper: rt},
		hist: hist,
	}
}

func (k *kubernetesAPIClientInstrumentation) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()

	req, ht := nethttp.TraceRequest(opentracing.GlobalTracer(), req)
	defer ht.Finish()

	resp, err := k.next.RoundTrip(req)
	duration := time.Since(start)
	statusCode := 0
	if resp != nil {
		statusCode = resp.StatusCode
	}
	instrument.ObserveWithExemplar(req.Context(), k.hist.WithLabelValues(urlToResourceDescription(req.URL.EscapedPath()), req.Method, strconv.Itoa(statusCode)), duration.Seconds())

	return resp, err
}

var (
	// Reference: https://kubernetes.io/docs/reference/using-api/api-concepts/#resource-uris
	groupAndVersion      = `(/api|/apis/(?P<group>[^/]+))/(?P<version>[^/]+)`
	typeAndName          = `(?P<type>[^/]+)(/(?P<name>[^/]+)(/(?P<subresource>[^/]+))?)?`
	namespacedPattern    = regexp.MustCompile(`^` + groupAndVersion + `/namespaces/[^/]+/` + typeAndName + `$`)
	nonNamespacedPattern = regexp.MustCompile(`^` + groupAndVersion + `/` + typeAndName + `$`)
)

func urlToResourceDescription(path string) string {
	match := namespacedPattern.FindStringSubmatch(path)
	pattern := namespacedPattern

	if match == nil {
		match = nonNamespacedPattern.FindStringSubmatch(path)
		pattern = nonNamespacedPattern

		if match == nil {
			// Path doesn't follow either expected pattern, give up.
			return path
		}
	}

	group := match[pattern.SubexpIndex("group")]
	version := match[pattern.SubexpIndex("version")]
	resourceType := match[pattern.SubexpIndex("type")]
	name := match[pattern.SubexpIndex("name")]
	subresourceType := match[pattern.SubexpIndex("subresource")]

	if group == "" {
		group = "core"
	}

	if subresourceType != "" {
		return fmt.Sprintf("%s/%s/%s object %s subresource", group, version, resourceType, subresourceType)
	} else if name == "" {
		return fmt.Sprintf("%s/%s/%s collection", group, version, resourceType)
	} else {
		return fmt.Sprintf("%s/%s/%s object", group, version, resourceType)
	}
}
