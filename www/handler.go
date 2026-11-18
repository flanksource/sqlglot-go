// Package www serves the sqlglot web UI embedded with the ui build tag.
package www

import (
	"fmt"
	"io/fs"
	"net/http"
	"strings"
)

// Handler serves the built assets and falls back to index.html for client-side
// routes. A missing file under assets/ returns 404.
func Handler() (http.Handler, error) {
	dist, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, err
	}
	if _, err := fs.Stat(dist, "index.html"); err != nil {
		return nil, fmt.Errorf("load embedded UI assets: %w (build with the ui tag and www/dist assets)", err)
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
