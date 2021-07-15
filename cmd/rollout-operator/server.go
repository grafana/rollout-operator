package main

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
)

type Server struct {
	srv    *http.Server
	mux    *http.ServeMux
	port   int
	logger log.Logger
}

// NewServer returns an instrumentation server
func NewServer(port int, logger log.Logger) *Server {
	mux := http.NewServeMux()

	return &Server{
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

func (s *Server) Start() error {
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

func (s *Server) Handle(pattern string, handler http.Handler) {
	s.mux.Handle(pattern, handler)
}
