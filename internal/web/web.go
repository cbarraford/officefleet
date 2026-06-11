// Package web serves the embedded operator SPA. The Vite build output lives
// in dist/ (untracked except .gitkeep); when the UI was not built, every SPA
// path serves an inline "UI not built" page so plain `go build` always works.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

const notBuiltPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>OfficeFleet</title></head>
<body style="font-family:sans-serif;background:#15171c;color:#e8eaed;display:grid;place-items:center;min-height:100vh;margin:0">
<div style="text-align:center"><h1>UI not built</h1>
<p>Run <code>make web</code> and rebuild the binary,<br>or use the dev server: <code>cd web &amp;&amp; npm run dev</code>.</p></div>
</body></html>`

// Mount registers the SPA on mux. The "/" pattern is ServeMux's
// lowest-precedence route, so explicit mounts (/api/v1, /webhooks, /healthz,
// and later /avatars) always win regardless of registration order.
func Mount(mux *http.ServeMux) {
	dist, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err) // unreachable: embed guarantees dist exists
	}
	mountFS(mux, dist)
}

// mountFS is Mount with an injectable filesystem so tests can exercise both
// the built (fstest.MapFS) and not-built (real embed) modes.
func mountFS(mux *http.ServeMux, dist fs.FS) {
	index, err := fs.ReadFile(dist, "index.html")
	built := err == nil
	fileServer := http.FileServerFS(dist)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !built {
			serveHTML(w, []byte(notBuiltPage))
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name != "" && name != "index.html" {
			if info, serr := fs.Stat(dist, name); serr == nil && !info.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					// Vite content-hashes asset filenames — safe to cache forever.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// SPA fallback: client-routed paths (and the root) get index.html.
		serveHTML(w, index)
	})
}

func serveHTML(w http.ResponseWriter, page []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(page)
}
