package util

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// InstrumentationServer serves instrumentation endpoints on a different port
// than the default server
type InstrumentationServer struct {
	srv    *http.Server
	port   int
	logger log.Logger
}

// NewInstrumentationServer returns an instrumentation server
func NewInstrumentationServer(port int, reg prometheus.Gatherer, logger log.Logger) *InstrumentationServer {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	return &InstrumentationServer{
		port: port,
		srv: &http.Server{
			Handler:      mux,
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  3 * time.Minute,
		},
		logger: logger,
	}
}

func (i *InstrumentationServer) Start() error {
	// Setup listeners first, so we can fail early if the port is in use.
	httpListener, err := net.Listen("tcp", fmt.Sprintf(":%d", i.port))
	if err != nil {
		return err
	}

	go func() {
		if err := i.srv.Serve(httpListener); err != nil {
			level.Error(i.logger).Log("msg", "metrics server terminated", "err", err)
		}
	}()

	return nil
}
