// Command status-ui-preview serves the rollout status UI with demo data.
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
	"github.com/grafana/rollout-operator/pkg/status"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "Listen address for the status UI preview server.")
	flag.Parse()

	ui, err := frontend.New(status.DemoReader{})
	if err != nil {
		log.Fatal(err)
	}

	r := mux.NewRouter()
	ui.Register(r)
	r.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, frontend.BasePath+"/", http.StatusFound)
	})

	url := fmt.Sprintf("http://%s%s/", *addr, frontend.BasePath)
	log.Printf("serving status UI preview with demo data at %s", url)
	log.Fatal(http.ListenAndServe(*addr, r))
}
