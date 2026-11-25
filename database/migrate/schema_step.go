package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/flanksource/commons/logger"
	"github.com/flanksource/sqlglot-go/database/migrate/dialect/atlasdriver"
	native "github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// schemaStep is the schema part of a PostgreSQL or SQLite bundle: the desired
// schema the bundle's HCL declares, planned against the live database and
// applied unit by unit through the engine.
type schemaStep struct {
	Dialect string
	// Schema is the one schema the step manages in the database.
	Schema  string
	Desired native.Schema
	// AllowDrops widens the scope from the declared objects to the whole schema.
	AllowDrops bool
	// Exclude hides objects from inspection (Atlas glob patterns); the desired
	// schema has already had them removed.
	Exclude []string
}

// scope is declared by default, so an undeclared table is never dropped, and
// complete under WithDrops.
func (s schemaStep) scope() engine.Scope {
	if s.AllowDrops {
		return engine.Scope{Mode: engine.ScopeComplete, Schemas: []string{s.Schema}}
	}
	return engine.Scope{Mode: engine.ScopeDeclared, Schemas: []string{s.Schema}}
}

// plan reads the live schema and the dependents the planned changes force, and
// plans the changes the scope allows, limited to the selection.
func (s schemaStep) plan(ctx context.Context, db *sql.DB, selection changeSelection) (engine.Artifact, error) {
	artifact, err := engine.PlanLive(atlasdriver.WithExclude(ctx, s.Exclude), db, engine.PlanLiveRequest{
		Dialect: s.Dialect, Scope: s.scope(), Desired: s.Desired, Selection: selection.IDs, SelectNone: selection.None,
	})
	if err != nil {
		return artifact, fmt.Errorf("plan %s schema changes: %w", s.Dialect, err)
	}
	return artifact, nil
}

// planUnreviewed is the plan an unreviewed apply runs: every planned change, but
// for the column drops of declared tables, which WithDrops alone allows.
func (s schemaStep) planUnreviewed(ctx context.Context, db *sql.DB) (unreviewedSelection, error) {
	full, err := s.plan(ctx, db, changeSelection{})
	if err != nil || s.AllowDrops {
		return unreviewedSelection{Artifact: &full}, err
	}
	return selectUnreviewed(full, true, func(selection changeSelection) (engine.Artifact, error) { return s.plan(ctx, db, selection) })
}

// run plans and applies the step. It is the unreviewed path: it acknowledges
// every destructive change of its own plan, so the scope and the declaration are
// the only bounds on what it may destroy. Lock contention is retried; units that
// committed stay committed and a retry skips them, because each unit is
// classified against the database before it runs.
func (s schemaStep) run(ctx context.Context, db *sql.DB) error {
	selected, err := s.planUnreviewed(ctx, db)
	if err != nil {
		return err
	}
	log := logger.GetLogger("migrate")
	for _, warning := range selected.Warnings {
		log.Warnf("%s", warning)
	}
	artifact := *selected.Artifact
	if len(artifact.Units) == 0 {
		log.Debugf("No schema changes detected")
		return nil
	}
	request := engine.ApplyRequest{
		Artifact: artifact, Hash: artifact.Hash, Acknowledgements: engine.Acknowledgements(artifact), LockTimeout: lockTimeout,
	}
	err = retryOnLockContention(ctx, "apply schema changes", func() error {
		_, err := engine.Apply(atlasdriver.WithExclude(ctx, s.Exclude), db, request)
		return err
	})
	if err != nil {
		return fmt.Errorf("apply %d schema changes in %d units: %w", len(artifact.Changes), len(artifact.Units), err)
	}
	log.V(1).Infof("Applied %d schema changes in %d units", len(artifact.Changes), len(artifact.Units))
	return nil
}

// lockTimeout is how long a unit waits for a lock before the database cancels
// it and the apply retries. It is the policy the script phases use
// (migrationLockTimeout); a variable so a spec can shorten it.
var lockTimeout = func() time.Duration {
	timeout, err := time.ParseDuration(migrationLockTimeout)
	if err != nil {
		panic(fmt.Sprintf("migrationLockTimeout %q is not a duration: %v", migrationLockTimeout, err))
	}
	return timeout
}()
