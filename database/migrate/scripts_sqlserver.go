package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/database/atlas/sqlserver"
	backend "github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver"
)

// sqlServerBundle is what a SQL Server migration declares: the dbo realm of HCL
// tables and routines, and the hash-gated scripts.
type sqlServerBundle struct {
	realm   *schema.Realm
	scripts map[string]*script
}

// loadSQLServerBundle loads the realm and the scripts, splitting each script
// into the batches sqlcmd would send.
func loadSQLServerBundle(schemaFS fs.FS, cfg options) (sqlServerBundle, error) {
	realm, err := sqlServerDesired(schemaFS, cfg)
	if err != nil {
		return sqlServerBundle{}, err
	}
	scripts, err := loadScripts(schemaFS, cfg.dir, cfg.routines)
	if err != nil {
		return sqlServerBundle{}, err
	}
	for _, s := range scripts {
		if _, err = backend.Batches(s.path, s.content); err != nil {
			return sqlServerBundle{}, err
		}
	}
	target := realm.Schemas[0]
	if len(target.Tables)+len(target.Procs)+len(target.Funcs)+len(scripts) == 0 {
		return sqlServerBundle{}, fmt.Errorf("SQL Server migration bundle %q declares no tables, routines or scripts", cfg.name)
	}
	return sqlServerBundle{realm: realm, scripts: scripts}, nil
}

func (b sqlServerBundle) declaresObjects() bool {
	target := b.realm.Schemas[0]
	return len(target.Tables)+len(target.Procs)+len(target.Funcs) > 0
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
// statements that reconcile the declared tables and routines, and the scripts
// it would run. It inspects only the objects the bundle names.
type SQLServerPending struct {
	Statements []string         `json:"statements,omitempty"`
	Scripts    []PendingScript  `json:"scripts,omitempty"`
	ScriptPlan *backend.Preview `json:"scriptPlan,omitempty"`
}

// Changed reports whether Apply would change anything beyond running its
// `runs: always` scripts.
func (p SQLServerPending) Changed() bool {
	if len(p.Statements) > 0 {
		return true
	}
	for _, s := range p.Scripts {
		if s.Reason != "always" {
			return true
		}
	}
	return false
}

// PendingSQLServer plans Apply on db without executing or recording anything.
func PendingSQLServer(ctx context.Context, db *sql.DB, schemaFS fs.FS, opts ...Option) (SQLServerPending, error) {
	cfg := resolveOptions(opts)
	bundle, err := loadSQLServerBundle(schemaFS, cfg)
	if err != nil {
		return SQLServerPending{}, err
	}
	var pending SQLServerPending
	if bundle.declaresObjects() {
		if pending.Statements, err = pendingSQLServerStatements(ctx, db, bundle, cfg); err != nil {
			return pending, err
		}
	}
	options, err := scriptOptions(db, bundle, cfg)
	if err != nil {
		return pending, err
	}
	preview, err := backend.Inspect(ctx, options, "")
	if err != nil {
		return pending, err
	}
	pending.ScriptPlan = &preview
	for _, item := range preview.Plan.Scripts {
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

func pendingSQLServerStatements(ctx context.Context, db *sql.DB, bundle sqlServerBundle, cfg options) ([]string, error) {
	client, err := sqlserver.OpenClient(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("open SQL Server migration database: %w", err)
	}
	changes, err := sqlServerChanges(ctx, client, bundle.realm, cfg.exclude)
	if err != nil || len(changes) == 0 {
		return nil, err
	}
	plan, err := client.PlanChanges(ctx, "", changes)
	if err != nil {
		return nil, fmt.Errorf("plan %d schema changes: %w", len(changes), err)
	}
	statements := make([]string, len(plan.Changes))
	for i, change := range plan.Changes {
		statements[i] = change.Cmd
	}
	return statements, nil
}
