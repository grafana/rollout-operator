package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
)

func main() {
	alive := &atomic.Int64{}
	alive.Store(1)
	ready := &atomic.Int64{}
	if os.Getenv("READY") == "true" {
		ready.Store(1)
	}

	mux := http.NewServeMux()
	mux.Handle("/ready", probeHandler(ready))
	mux.Handle("/alive", probeHandler(alive))

	if prefix := os.Getenv("PREFIX"); prefix != "" {
		log.Printf("prefix=%s", prefix)
		mux.Handle(prefix+"/ready", probeHandler(ready))
		mux.Handle(prefix+"/alive", probeHandler(alive))
	}

	srv := http.Server{Handler: logged(mux), Addr: "0.0.0.0:8080"}
	closed := make(chan struct{})
	go func() {
		sigint := make(chan os.Signal, 1)
		signal.Notify(sigint, syscall.SIGINT, syscall.SIGTERM)
		log.Printf("Received shutdown signal: %d", <-sigint)
		if err := srv.Shutdown(context.Background()); err != nil {
			log.Printf("Can't shutdown HTTP server: %v", err)
		}
		close(closed)
	}()

	log.Print("Serving on 0.0.0.0:8080...")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("Can't ListenAndServe: %v", err)
	}
	<-closed
}

func probeHandler(probe *atomic.Int64) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodPost:
			probe.Store(1)
		case http.MethodDelete:
			probe.Store(0)
		case http.MethodGet:
			if probe.Load() == 0 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		w.WriteHeader(http.StatusOK)
	}
}

func logged(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(loggingResponseWriter{w, r.Method, r.URL.Path}, r)
	}
}

type loggingResponseWriter struct {
	http.ResponseWriter
	method, path string
}

func (lw loggingResponseWriter) WriteHeader(statusCode int) {
	log.Printf("%d %s %s", statusCode, lw.method, lw.path)
	lw.ResponseWriter.WriteHeader(statusCode)
}
