package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/gorilla/mux"

	"github.com/grafana/rollout-operator/pkg/tlscert"
)

type tlsServer struct {
	srv    *http.Server
	mux    *mux.Router
	port   int
	logger log.Logger
}

func newTLSServer(cfg config, logger log.Logger, cert tlscert.Certificate, metrics *metrics) (*tlsServer, error) {
	pair, err := tls.X509KeyPair(cert.Cert, cert.Key)
	if err != nil {
		return nil, fmt.Errorf("failed to load key pair: %v", err)
	}

	m, handler := newInstrumentedRouter(metrics, cfg, logger)

	return &tlsServer{
		port: cfg.serverTLSPort,
		mux:  m,
		srv: &http.Server{
			TLSConfig:    &tls.Config{Certificates: []tls.Certificate{pair}},
			Handler:      handler,
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  3 * time.Minute,
		},
		logger: logger,
	}, nil
}

func (s *tlsServer) Start() error {
	// Setup listeners first, so we can fail early if the port is in use.
	httpListener, err := net.Listen("tcp", fmt.Sprintf(":%d", s.port))
	if err != nil {
		return err
	}

	go func() {
		if err := s.srv.ServeTLS(httpListener, "", ""); err != nil {
			level.Error(s.logger).Log("msg", "tls server terminated", "err", err)
		}
	}()

	return nil
}

func (s *tlsServer) Handle(pattern string, handler http.Handler) {
	s.mux.Handle(pattern, handler)
}
