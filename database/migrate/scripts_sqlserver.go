package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	backend "github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver"
)

// sqlServerBundle is what a SQL Server migration declares: the dbo realm of HCL
// tables and routines, and the hash-gated scripts.
type sqlServerBundle struct {
	realm   *schema.Realm
	scripts []scripts.Script
}

// loadSQLServerBundle loads the realm and the scripts for SQL Server planning.
func loadSQLServerBundle(schemaFS fs.FS, cfg options) (sqlServerBundle, error) {
	realm, err := sqlServerDesired(schemaFS, cfg)
	if err != nil {
		return sqlServerBundle{}, err
	}
	items, err := scripts.Load(scripts.LoadOptions{
		FS: schemaFS, Dir: cfg.dir, Skip: []string{cfg.routines}, Sources: cfg.scriptSources, AllowEmpty: true,
	})
	if err != nil {
		return sqlServerBundle{}, err
	}
	for _, item := range items {
		if _, err := backend.Batches(item.Path, item.SQL); err != nil {
			return sqlServerBundle{}, err
		}
	}
	target := realm.Schemas[0]
	if len(target.Tables)+len(target.Procs)+len(target.Funcs)+len(items) == 0 {
		return sqlServerBundle{}, fmt.Errorf("SQL Server migration bundle %q declares no tables, routines or scripts", cfg.name)
	}
	return sqlServerBundle{realm: realm, scripts: items}, nil
}

func (b sqlServerBundle) declaresObjects() bool {
	return len(b.declaredNames()) > 0
}

// PendingScript is one script Apply would run, and why: new or changed since
// it last ran, dependency (a script it depends on changed), or always (a
// `runs: always` script, which runs on every Apply and so is never a change
// by itself).
type PendingScript struct {
	Path    string   `json:"path"`
	Phase   string   `json:"phase"`
	Reason  string   `json:"reason"` // new | changed | dependency | always
	Reasons []string `json:"reasons,omitempty"`
	Blocked []string `json:"blocked,omitempty"`
	Hash    string   `json:"hash,omitempty"`
}

// SQLServerPending is what Apply would do to a SQL Server database: the
// statements that reconcile the declared tables and routines (every unit's
// statements in order, derived from the plan file's schema artifact), the
// scripts it would run, and the plan file holding both. It inspects only the
// objects the bundle names.
type SQLServerPending struct {
	Statements []string        `json:"statements,omitempty"`
	Scripts    []PendingScript `json:"scripts,omitempty"`
	// Warnings name each column of a declared table that is in the database but
	// not declared: Apply does not drop it and reports it every time.
	Warnings []string          `json:"warnings,omitempty"`
	Plan     *backend.PlanFile `json:"plan"`
}

// Changed reports whether Apply would change anything beyond running its
// `runs: always` scripts.
func (p SQLServerPending) Changed() bool { return p.Plan.Changed() }

// PendingSQLServer plans Apply on db without executing or recording anything.
func PendingSQLServer(ctx context.Context, db *sql.DB, schemaFS fs.FS, opts ...Option) (SQLServerPending, error) {
	file, warnings, err := planSQLServer(ctx, db, schemaFS, resolveOptions(opts))
	if err != nil {
		return SQLServerPending{}, err
	}
	pending := SQLServerPending{Plan: &file, Warnings: warnings}
	for _, statement := range file.SchemaStatements() {
		pending.Statements = append(pending.Statements, statement.SQL)
	}
	for _, item := range file.Actions {
		if !item.Selected && len(item.Blocked) == 0 {
			continue
		}
		reason := strings.Join(item.Reasons, ",")
		switch {
		case len(item.Blocked) > 0:
			reason = "blocked"
		case slices.Contains(item.Reasons, "new"):
			reason = "new"
		case slices.Contains(item.Reasons, "source-changed"):
			reason = "changed"
		case slices.Contains(item.Reasons, "script-dependency-changed"):
			reason = "dependency"
		}
		pending.Scripts = append(pending.Scripts, PendingScript{Path: item.Script.Path, Phase: string(item.Script.Phase), Reason: reason, Reasons: item.Reasons, Blocked: item.Blocked, Hash: item.State.Hash})
	}
	return pending, nil
}
