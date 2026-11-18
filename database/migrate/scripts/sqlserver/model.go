// Package sqlserver observes and executes dependency-aware scripts on SQL Server.
package sqlserver

import (
	"context"
	"database/sql"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
)

type Function struct {
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	Nullable      bool     `json:"nullable"`
	Prerequisites []string `json:"prerequisites,omitempty"`
}

type Options struct {
	DB        *sql.DB
	Scope     string
	Scripts   []scripts.Script
	Functions []Function
	analysis  map[string]scripts.Analysis
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type sessionDB interface {
	queryer
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type Preview struct {
	Plan       scripts.Plan `json:"plan"`
	Database   string       `json:"database"`
	BundleHash string       `json:"bundleHash"`
}
