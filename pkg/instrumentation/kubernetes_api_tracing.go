package instrumentation

import (
	"github.com/opentracing-contrib/go-stdlib/nethttp"
	"github.com/opentracing/opentracing-go"
	"net/http"
)

type kubernetesAPIClientTracer struct {
	next http.RoundTripper
}

func NewKubernetesAPIClientTracer(next http.RoundTripper) http.RoundTripper {
	return &kubernetesAPIClientTracer{next}
}

func (k *kubernetesAPIClientTracer) RoundTrip(req *http.Request) (*http.Response, error) {
	req, ht := nethttp.TraceRequest(opentracing.GlobalTracer(), req)
	defer ht.Finish()

	return k.next.RoundTrip(req)
}
