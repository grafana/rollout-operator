package main

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/gorilla/mux"
)

type server struct {
	srv    *http.Server
	mux    *mux.Router
	port   int
	logger log.Logger
}

func newServer(cfg config, logger log.Logger, metrics *metrics) *server {
	m, handler := newInstrumentedRouter(metrics, cfg, logger)

	return &server{
		port: cfg.serverPort,
		mux:  m,
		srv: &http.Server{
			Handler:      handler,
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  3 * time.Minute,
		},
		logger: logger,
	}
}

func (s *server) Start() error {
	// Setup listeners first, so we can fail early if the port is in use.
	httpListener, err := net.Listen("tcp", fmt.Sprintf(":%d", s.port))
	if err != nil {
		return err
	}

	go func() {
		if err := s.srv.Serve(httpListener); err != nil {
			level.Error(s.logger).Log("msg", "metrics server terminated", "err", err)
		}
	}()

	return nil
}

func (s *server) Handle(pattern string, handler http.Handler) {
	s.mux.Handle(pattern, handler)
}

func (s *server) PathPrefix(tpl string) *mux.Route {
	return s.mux.PathPrefix(tpl)
}
