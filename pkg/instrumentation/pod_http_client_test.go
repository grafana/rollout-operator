package instrumentation

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPodHTTPClientFollowsRedirectsPreservingMethod(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			var sawFinal atomic.Bool
			var methods []string

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				methods = append(methods, r.Method+" "+r.URL.Path)
				switch r.URL.Path {
				case "//ingester/prepare_shutdown":
					http.Redirect(w, r, "/ingester/prepare_shutdown", http.StatusMovedPermanently)
				case "/ingester/prepare_shutdown":
					if r.Method == method {
						sawFinal.Store(true)
						w.WriteHeader(http.StatusOK)
						return
					}
					w.WriteHeader(http.StatusMethodNotAllowed)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer ts.Close()

			client := NewPodHTTPClient(nil, "", nil, 0, nil)
			req, err := http.NewRequest(method, ts.URL+"//ingester/prepare_shutdown", nil)
			require.NoError(t, err)

			resp, err := client.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)

			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.True(t, sawFinal.Load(), "final request after redirect must remain %s", method)
			require.Equal(t, []string{
				method + " //ingester/prepare_shutdown",
				method + " /ingester/prepare_shutdown",
			}, methods)
		})
	}
}

func TestPodHTTPClientDoesNotFollowCrossHostRedirect(t *testing.T) {
	var requests int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Location", "http://example.com/elsewhere")
		w.WriteHeader(http.StatusMovedPermanently)
	}))
	defer ts.Close()

	client := NewPodHTTPClient(nil, "", nil, 0, nil)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/prepare", nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	require.Equal(t, http.StatusMovedPermanently, resp.StatusCode)
	require.Equal(t, 1, requests)
}

func TestPodHTTPClientReturnsNon2xxWithoutRedirect(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	client := NewPodHTTPClient(nil, "", nil, 0, nil)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/prepare", nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}
