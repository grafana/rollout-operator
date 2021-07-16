package main

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
)

type server struct {
	srv    *http.Server
	mux    *http.ServeMux
	port   int
	logger log.Logger
}

func newServer(port int, logger log.Logger) *server {
	mux := http.NewServeMux()

	return &server{
		port: port,
		mux:  mux,
		srv: &http.Server{
			Handler:      mux,
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
