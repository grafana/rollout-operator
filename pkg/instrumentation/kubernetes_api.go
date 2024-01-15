package instrumentation

import (
	"github.com/opentracing-contrib/go-stdlib/nethttp"
	"github.com/opentracing/opentracing-go"
	"net/http"
)

type kubernetesAPIClientInstrumentation struct {
	next http.RoundTripper
}

func InstrumentKubernetesAPIClient(next http.RoundTripper) http.RoundTripper {
	return &kubernetesAPIClientInstrumentation{next}
}

func (k *kubernetesAPIClientInstrumentation) RoundTrip(req *http.Request) (*http.Response, error) {
	req, ht := nethttp.TraceRequest(opentracing.GlobalTracer(), req)
	defer ht.Finish()

	return k.next.RoundTrip(req)
}
