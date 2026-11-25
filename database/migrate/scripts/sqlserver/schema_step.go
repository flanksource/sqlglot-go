package sqlserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	_ "github.com/flanksource/sqlglot-go/database/migrate/dialect/sqlserver" // registers the "sqlserver" engine dialect
	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	native "github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

const (
	schemaDialect = "sqlserver"
	// defaultSchemaLockTimeout bounds every wait of a schema unit, the migration
	// lock included. It equals the scripts' own statement lock timeout
	// (setLockTimeout), so a blocked DDL fails as fast as a blocked script.
	defaultSchemaLockTimeout = 15 * time.Second
)

// SchemaStatement is one statement of the schema step as the artifact plans it.
type SchemaStatement struct {
	Unit        string
	Change      string
	SQL         string
	Destructive bool
}

// SchemaStatements lists the statements the schema step runs, units in
// artifact order and statements in op order; nil when the step plans none.
func (p PlanFile) SchemaStatements() []SchemaStatement {
	a := p.Bundle.Schema
	if a == nil {
		return nil
	}
	var out []SchemaStatement
	for _, u := range a.Units {
		for _, op := range u.Ops {
			destructive := slices.ContainsFunc(a.Changes, func(c native.Change) bool { return c.ID == op.Change && c.Destructive })
			for _, s := range op.Statements {
				out = append(out, SchemaStatement{Unit: u.ID, Change: op.Change, SQL: s.SQL, Destructive: destructive})
			}
		}
	}
	return out
}

// schemaWrites is what the step changes: every declared object and every object
// of a unit, once the plan has a unit to run.
func schemaWrites(a *engine.Artifact) []scripts.ObjectName {
	if a == nil || len(a.Units) == 0 {
		return nil
	}
	var out []scripts.ObjectName
	add := func(schema, name string) {
		if object := scripts.NewObjectName(schema, name); !slices.Contains(out, object) {
			out = append(out, object)
		}
	}
	for _, o := range a.Scope.Objects {
		add(o.Schema, o.Name)
	}
	for _, u := range a.Units {
		for _, o := range u.Objects {
			add(o.Schema, o.Name)
		}
	}
	return out
}

// validateSchema refuses an artifact that is not a SQL Server artifact or does
// not match its own hash, snapshots and planner version.
func validateSchema(a *engine.Artifact) error {
	if a == nil {
		return nil
	}
	if a.Dialect != schemaDialect {
		return fmt.Errorf("schema artifact is for dialect %q, not %q", a.Dialect, schemaDialect)
	}
	if len(a.Scope.Objects) == 0 {
		return errors.New("schema artifact declares no objects")
	}
	return engine.Validate(*a)
}

// currentSchema answers whether the database still holds what the artifact was
// planned against: the same capabilities, and every unit applied or pending. A
// database that moved is a *StalePlanError.
func currentSchema(ctx context.Context, q engine.Querier, a *engine.Artifact) error {
	if a == nil {
		return nil
	}
	if err := sameCapabilities(ctx, q, *a); err != nil {
		return err
	}
	_, err := engine.Check(ctx, q, *a)
	return staleSchema(err)
}

// sameCapabilities mirrors the engine's own check, which reports a mismatch as
// an untyped error, so that the plan file reports one stale-plan condition. An
// empty fingerprint marks an artifact planned offline.
func sameCapabilities(ctx context.Context, q engine.Querier, a engine.Artifact) error {
	if a.Capabilities.Fingerprint == "" {
		return nil
	}
	d, err := engine.Lookup(a.Dialect)
	if err != nil {
		return err
	}
	now, err := d.Capabilities(ctx, q)
	if err != nil {
		return fmt.Errorf("read %s capabilities: %w", a.Dialect, err)
	}
	if now.Fingerprint != a.Capabilities.Fingerprint {
		return &StalePlanError{Moved: []string{fmt.Sprintf("database capabilities changed since the plan (planned against %q, now %q)", a.Capabilities.Fingerprint, now.Fingerprint)}}
	}
	return nil
}

// staleSchema maps the engine's drift onto the plan file's stale-plan error.
func staleSchema(err error) error {
	var drift *engine.DriftError
	if errors.As(err, &drift) {
		return &StalePlanError{Moved: []string{fmt.Sprintf("declared schema unit %s moved since the plan: %d catalog changes", drift.Unit, len(drift.Changes))}}
	}
	return err
}

// runSchema executes the artifact's units through the engine on connections of
// db other than q, and returns how many units committed. Units already at their
// desired state are skipped, so a re-run of an interrupted step resumes after
// the units that committed.
func runSchema(ctx context.Context, db *sql.DB, q engine.Querier, a engine.Artifact, timeout time.Duration) (int, error) {
	if db.Stats().MaxOpenConnections == 1 {
		return 0, errors.New("schema step needs a second pooled connection beside the locked apply connection: the pool allows one")
	}
	if err := sameCapabilities(ctx, q, a); err != nil {
		return 0, err
	}
	result, err := engine.Apply(ctx, db, engine.ApplyRequest{Artifact: a, Hash: a.Hash, Acknowledgements: engine.Acknowledgements(a), LockTimeout: timeout})
	applied := 0
	for _, u := range result.Units {
		if u.Outcome == engine.Applied {
			applied++
		}
	}
	return applied, staleSchema(err)
}
