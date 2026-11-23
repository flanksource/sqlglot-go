package migrate

import (
	"database/sql"
	"fmt"
	"slices"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	backend "github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver"
)

type ScriptPolicy struct {
	Functions            []backend.Function
	Dependencies         map[string][]scripts.Dependency
	ExplicitDependencies []string
}

// WithScriptPolicy enables SQL Server dependency tracking for named script paths.
func WithScriptPolicy(policy ScriptPolicy) Option {
	return func(o *options) { o.scriptPolicy = &policy }
}

func refuseScriptPolicy(cfg options, dialect string) error {
	if cfg.scriptPolicy != nil {
		return fmt.Errorf("%s migrations do not support script dependency policies; only SQL Server does", dialect)
	}
	return nil
}

func scriptOptions(db *sql.DB, bundle sqlServerBundle, cfg options) (backend.Options, error) {
	items := bundle.scripts
	opts := backend.Options{DB: db, Scope: migrationScope(sqlServerSchema, cfg.name), Scripts: items}
	if cfg.scriptPolicy == nil {
		return opts, nil
	}
	opts.Functions = cfg.scriptPolicy.Functions
	declared := make(map[string]bool, len(items))
	for i := range items {
		id := items[i].ID
		declared[id] = true
		items[i].Dependencies = cfg.scriptPolicy.Dependencies[id]
		items[i].ExplicitDependencies = slices.Contains(cfg.scriptPolicy.ExplicitDependencies, id)
	}
	for id := range cfg.scriptPolicy.Dependencies {
		if !declared[id] {
			return opts, fmt.Errorf("script policy references missing script %s", id)
		}
	}
	for _, id := range cfg.scriptPolicy.ExplicitDependencies {
		if !declared[id] {
			return opts, fmt.Errorf("explicit policy references missing script %s", id)
		}
	}
	return opts, nil
}
