// Package sqlserver plans and applies dependency-aware scripts on SQL Server
// through a self-contained plan file: Plan observes the target once and
// writes what apply will do, and Apply runs exactly that plan, refusing it
// as stale when the target moved since.
package sqlserver

import (
	"context"
	"database/sql"
	"time"

	"github.com/flanksource/clicky/cache"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

type Function struct {
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	Nullable      bool     `json:"nullable"`
	Prerequisites []string `json:"prerequisites,omitempty"`
}

type Options struct {
	DB    *sql.DB
	Scope string
	// AbsorbScopes, when set, merges the ledgers of the other scopes it accepts
	// into Scope: Plan reads the newest row of each path across Scope and those
	// scopes, and Apply moves them into Scope. The plan file records the
	// scopes it absorbed.
	AbsorbScopes func(scope string) bool
	Scripts      []scripts.Script
	Functions    []Function
	// Schema is the reviewed declared-schema reconciliation, an engine artifact
	// of the "sqlserver" dialect, that runs between the pre and post scripts;
	// nil when the bundle declares no objects.
	Schema *engine.Artifact
	// Warnings are the caller's own warnings about the bundle; the plan file
	// carries them before the plan's.
	Warnings []string
	// Analysis caches script and module-definition analyses by content
	// (scripts.AnalysisKey); nil analyses every body on every call.
	Analysis cache.Store
	// AnalysisTTL is the expiry of each analysis written to Analysis; zero
	// writes without expiry.
	AnalysisTTL time.Duration
	analysis    map[string]scripts.Analysis
	absorbed    []string
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type sessionDB interface {
	queryer
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}
