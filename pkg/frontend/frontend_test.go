package frontend

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"

	"github.com/grafana/rollout-operator/pkg/status"
)

type fakeReader struct {
	snap *status.Snapshot
	err  error
}

func (f fakeReader) Snapshot(context.Context) (*status.Snapshot, error) {
	return f.snap, f.err
}

func newTestFrontend(t *testing.T, reader status.Reader) *Frontend {
	t.Helper()
	f, err := New(reader)
	require.NoError(t, err)
	return f
}

func TestFrontendIndex(t *testing.T) {
	snap := &status.Snapshot{
		Namespace:  "mimir",
		ObservedAt: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC),
		Groups: []status.Group{{
			Name:   "ingester",
			Phase:  status.PhaseProgressing,
			Reason: "0 of 3 pods updated",
			Members: []status.Member{{
				Name:            "ingester-zone-a",
				DesiredReplicas: 3,
				ReadyReplicas:   3,
				CurrentRevision: "prev",
				UpdateRevision:  "next",
				UpdatedPods:     0,
				TotalPods:       3,
				Phase:           status.PhaseProgressing,
				Reason:          "0 of 3 pods updated",
			}},
		}},
	}

	f := newTestFrontend(t, fakeReader{snap: snap})
	router := mux.NewRouter()
	f.Register(router)

	req := httptest.NewRequest(http.MethodGet, "/ui/status/", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	body := rec.Body.String()
	require.Contains(t, body, "ingester")
	require.Contains(t, body, "ingester-zone-a")
	require.Contains(t, body, "progressing")
	require.Contains(t, body, "Namespace <code>mimir</code>")
	require.Contains(t, body, "Read-only")
	require.NotContains(t, body, "<form")
	require.NotContains(t, body, "method=\"post\"")
}

func TestFrontendEmpty(t *testing.T) {
	f := newTestFrontend(t, fakeReader{snap: &status.Snapshot{Namespace: "mimir", Groups: []status.Group{}}})
	router := mux.NewRouter()
	f.Register(router)

	req := httptest.NewRequest(http.MethodGet, "/ui/status/", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "No StatefulSets with a")
}

func TestFrontendUnavailable(t *testing.T) {
	f := newTestFrontend(t, fakeReader{err: status.ErrUnavailable})
	router := mux.NewRouter()
	f.Register(router)

	req := httptest.NewRequest(http.MethodGet, "/ui/status/", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "still starting")
}

func TestFrontendSnapshotError(t *testing.T) {
	f := newTestFrontend(t, fakeReader{err: errors.New(`boom <script>alert("x")</script>`)})
	router := mux.NewRouter()
	f.Register(router)

	req := httptest.NewRequest(http.MethodGet, "/ui/status/", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	body := rec.Body.String()
	require.Contains(t, body, "Failed to load rollout status.")
	require.NotContains(t, body, "boom")
	require.NotContains(t, body, `<script>alert("x")</script>`)
}

func TestFrontendEscapesMemberFields(t *testing.T) {
	snap := &status.Snapshot{
		Namespace: "ns",
		Groups: []status.Group{{
			Name:  `<img src=x onerror=alert(1)>`,
			Phase: status.PhaseComplete,
			Members: []status.Member{{
				Name:   `<script>evil()</script>`,
				Reason: `ok & <b>bold</b>`,
				Phase:  status.PhaseComplete,
			}},
		}},
	}
	f := newTestFrontend(t, fakeReader{snap: snap})
	router := mux.NewRouter()
	f.Register(router)

	req := httptest.NewRequest(http.MethodGet, "/ui/status/", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	body := rec.Body.String()
	require.NotContains(t, body, "<script>evil()</script>")
	require.Contains(t, body, "&lt;script&gt;evil()&lt;/script&gt;")
	require.Contains(t, body, "&lt;img src=x onerror=alert(1)&gt;")
	require.Contains(t, body, "ok &amp; &lt;b&gt;bold&lt;/b&gt;")
}

func TestFrontendStaticAsset(t *testing.T) {
	f := newTestFrontend(t, fakeReader{snap: &status.Snapshot{}})
	router := mux.NewRouter()
	f.Register(router)

	req := httptest.NewRequest(http.MethodGet, "/ui/status/static/status.css", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), ".phase-complete")
}

func TestFrontendHead(t *testing.T) {
	f := newTestFrontend(t, fakeReader{snap: &status.Snapshot{Namespace: "mimir"}})
	router := mux.NewRouter()
	f.Register(router)

	req := httptest.NewRequest(http.MethodHead, "/ui/status/", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Empty(t, rec.Body.String())
}

func TestFrontendRejectsMutations(t *testing.T) {
	f := newTestFrontend(t, fakeReader{snap: &status.Snapshot{}})
	router := mux.NewRouter()
	f.Register(router)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/ui/status/", strings.NewReader("x"))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
			require.Equal(t, "GET, HEAD", rec.Header().Get("Allow"))
		})
	}
}

func TestNewRequiresReader(t *testing.T) {
	_, err := New(nil)
	require.Error(t, err)
}
