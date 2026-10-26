//go:build !ui

// Package www serves the sqlglot web UI. Without the ui build tag the binary
// carries no assets, so plain `go build` and `go test` never need node.
package www

import (
	"errors"
	"net/http"
)

func Handler() (http.Handler, error) {
	return nil, errors.New("sqlglot built without the ui tag; build it with make build or make install")
}
