package web

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func builtFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":        &fstest.MapFile{Data: []byte("<html><body>spa-index</body></html>")},
		"assets/app-abc.js": &fstest.MapFile{Data: []byte("console.log('app')")},
	}
}

// serveFS mounts dist alongside probe routes that must keep precedence.
func serveFS(t *testing.T, dist fs.FS, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("probe-ok"))
	})
	mux.HandleFunc("/api/v1/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "api-404", http.StatusNotFound)
	})
	mux.HandleFunc("POST /webhooks/{plugin}", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("webhook-ok"))
	})
	mountFS(mux, dist)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func body(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	b, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNotBuiltServesFallbackPage(t *testing.T) {
	// The real embedded FS holds only .gitkeep in a fresh clone — Mount must
	// serve the inline "UI not built" page, never error.
	mux := http.NewServeMux()
	Mount(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := body(t, rec); !strings.Contains(got, "UI not built") {
		t.Errorf("body = %q, want the not-built page", got)
	}
}

func TestNotBuiltFallbackPage(t *testing.T) {
	rec := serveFS(t, fstest.MapFS{}, http.MethodGet, "/agents/123")
	if rec.Code != http.StatusOK || !strings.Contains(body(t, rec), "UI not built") {
		t.Errorf("not-built mode = %d %q, want the inline page", rec.Code, body(t, rec))
	}
}

func TestRootServesIndex(t *testing.T) {
	rec := serveFS(t, builtFS(), http.MethodGet, "/")
	if rec.Code != http.StatusOK || !strings.Contains(body(t, rec), "spa-index") {
		t.Fatalf("GET / = %d %q, want 200 index", rec.Code, body(t, rec))
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("index Cache-Control = %q, want no-cache", cc)
	}
}

func TestSPAFallbackServesIndex(t *testing.T) {
	for _, path := range []string{"/agents/123", "/duties", "/login", "/settings"} {
		rec := serveFS(t, builtFS(), http.MethodGet, path)
		if rec.Code != http.StatusOK || !strings.Contains(body(t, rec), "spa-index") {
			t.Errorf("GET %s = %d, want index fallback", path, rec.Code)
		}
	}
}

func TestAssetContentTypeAndCache(t *testing.T) {
	rec := serveFS(t, builtFS(), http.MethodGet, "/assets/app-abc.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q, want javascript", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, want immutable", cc)
	}
}

func TestExplicitMountsKeepPrecedence(t *testing.T) {
	if got := body(t, serveFS(t, builtFS(), http.MethodGet, "/healthz")); got != "probe-ok" {
		t.Errorf("/healthz = %q, want probe-ok (SPA must not shadow it)", got)
	}
	rec := serveFS(t, builtFS(), http.MethodGet, "/api/v1/nonexistent")
	if rec.Code != http.StatusNotFound || !strings.Contains(body(t, rec), "api-404") {
		t.Errorf("/api/v1/* reached the SPA fallback: %d %q", rec.Code, body(t, rec))
	}
	if got := body(t, serveFS(t, builtFS(), http.MethodPost, "/webhooks/gitlab")); got != "webhook-ok" {
		t.Errorf("POST /webhooks/gitlab = %q, want webhook-ok (SPA 405 must not shadow it)", got)
	}
}

func TestDirectoryNotListed(t *testing.T) {
	rec := serveFS(t, builtFS(), http.MethodGet, "/assets/")
	if got := body(t, rec); strings.Contains(got, "app-abc.js") {
		t.Errorf("directory listing leaked: %q", got)
	}
}

func TestNonGetRejected(t *testing.T) {
	rec := serveFS(t, builtFS(), http.MethodPost, "/")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, want 405", rec.Code)
	}
}
