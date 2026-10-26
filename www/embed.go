//go:build ui

// Package www serves the sqlglot web UI built into dist/ by `make build`.
package www

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Handler serves the built assets and falls back to index.html for client-side
// routes; a missing file under assets/ is a 404, not the SPA.
func Handler() (http.Handler, error) {
	dist, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, err
	}
	if _, err := fs.Stat(dist, "index.html"); err != nil {
		return nil, err
	}
	fileServer := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if f, err := dist.Open(path); err == nil {
			_ = f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(path, "assets/") {
			http.NotFound(w, r)
			return
		}
		spa := r.Clone(r.Context())
		spa.URL.Path = "/"
		fileServer.ServeHTTP(w, spa)
	}), nil
}
