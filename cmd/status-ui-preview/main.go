// Command status-ui-preview serves the rollout status UI with demo Kubernetes objects.
//
//	go run ./cmd/status-ui-preview
//	# or: make status-ui-preview
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/gorilla/mux"

	"github.com/grafana/rollout-operator/pkg/frontend"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "Listen address for the status UI preview server.")
	flag.Parse()

	c, err := newDemoController()
	if err != nil {
		log.Fatal(err)
	}
	defer c.Stop()

	ui, err := frontend.New(c)
	if err != nil {
		log.Fatal(err)
	}

	r := mux.NewRouter()
	ui.Register(r)
	r.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/ui/", http.StatusFound)
	})

	url := fmt.Sprintf("http://%s/ui/", *addr)
	log.Printf("serving status UI preview from mock StatefulSets/Pods at %s", url)
	log.Fatal(http.ListenAndServe(*addr, r))
}
