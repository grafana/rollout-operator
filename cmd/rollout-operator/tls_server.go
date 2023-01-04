package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"

	"github.com/grafana/rollout-operator/pkg/tlscert"
)

type tlsServer struct {
	srv    *http.Server
	mux    *http.ServeMux
	port   int
	logger log.Logger
}

func newTLSServer(port int, logger log.Logger, cert tlscert.Certificate) (*tlsServer, error) {
	pair, err := tls.X509KeyPair(cert.Cert, cert.Key)
	if err != nil {
		return nil, fmt.Errorf("failed to load key pair: %v", err)
	}

	mux := http.NewServeMux()

	return &tlsServer{
		port: port,
		mux:  mux,
		srv: &http.Server{
			TLSConfig:    &tls.Config{Certificates: []tls.Certificate{pair}},
			Handler:      mux,
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
