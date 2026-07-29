package frontend

import (
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"time"

	"github.com/gorilla/mux"

	"github.com/grafana/rollout-operator/pkg/status"
)

//go:embed templates/*.gohtml static/*
var embeddedFS embed.FS

// Frontend serves the read-only rollout status UI.
type Frontend struct {
	reader    status.Reader
	templates *template.Template
	static    http.Handler
}

// New creates a Frontend backed by the given status reader.
func New(reader status.Reader) (*Frontend, error) {
	if reader == nil {
		return nil, errors.New("status reader is required")
	}

	tmpl, err := template.New("").Funcs(template.FuncMap{
		"phaseClass": phaseClass,
	}).ParseFS(embeddedFS, "templates/*.gohtml")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	staticFS, err := fs.Sub(embeddedFS, "static")
	if err != nil {
		return nil, fmt.Errorf("static assets: %w", err)
	}

	return &Frontend{
		reader:    reader,
		templates: tmpl,
		static:    http.FileServer(http.FS(staticFS)),
	}, nil
}

// BasePath is the URL prefix where the status UI is mounted.
const BasePath = "/ui/status"

// Register mounts the status UI under /ui/status on the given router.
// /ui and /ui/ redirect to /ui/status/ while it is the only page.
func (f *Frontend) Register(r *mux.Router) {
	uiRouter := r.PathPrefix("/ui").Subrouter()
	uiRouter.Use(readOnlyMiddleware)
	uiRouter.Use(securityHeadersMiddleware)

	statusRouter := uiRouter.PathPrefix("/status").Subrouter()
	statusRouter.HandleFunc("/", f.handleIndex)
	statusRouter.HandleFunc("", f.handleIndex)
	statusRouter.PathPrefix("/static/").Handler(http.StripPrefix(BasePath+"/static/", f.static))

	uiRouter.HandleFunc("/", redirectToStatus)
	uiRouter.HandleFunc("", redirectToStatus)
}

func redirectToStatus(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, BasePath+"/", http.StatusFound)
}

type pageData struct {
	Title       string
	Namespace   string
	ObservedAt  time.Time
	Groups      []status.Group
	Empty       bool
	Error       string
	Unavailable bool
}

func (f *Frontend) handleIndex(w http.ResponseWriter, r *http.Request) {
	data := pageData{Title: "Rollout status"}
	code := http.StatusOK

	snap, err := f.reader.Snapshot(r.Context())
	switch {
	case errors.Is(err, status.ErrUnavailable):
		data.Unavailable = true
		data.Error = "Status is not available yet; the operator is still starting."
		code = http.StatusServiceUnavailable
	case err != nil:
		// Avoid leaking Kubernetes API details to clients that can reach the metrics port.
		data.Error = "Failed to load rollout status."
		code = http.StatusInternalServerError
	case snap == nil:
		data.Error = "Failed to load rollout status."
		code = http.StatusInternalServerError
	default:
		data.Namespace = snap.Namespace
		data.ObservedAt = snap.ObservedAt
		data.Groups = snap.Groups
		data.Empty = len(snap.Groups) == 0
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if r.Method == http.MethodHead {
		return
	}
	_ = f.templates.ExecuteTemplate(w, "index.gohtml", data)
}

func readOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; img-src 'self' data:; frame-ancestors 'none'; form-action 'none'; base-uri 'self'")
		next.ServeHTTP(w, r)
	})
}

func phaseClass(phase status.Phase) string {
	switch phase {
	case status.PhaseComplete:
		return "phase-complete"
	case status.PhaseProgressing:
		return "phase-progressing"
	case status.PhaseWaiting:
		return "phase-waiting"
	case status.PhasePaused:
		return "phase-paused"
	case status.PhaseDegraded:
		return "phase-degraded"
	default:
		return "phase-unknown"
	}
}
