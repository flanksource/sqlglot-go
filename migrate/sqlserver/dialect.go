package sqlserver

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// FeatureTryConvert is the capability feature that says TRY_CONVERT exists,
// which is database compatibility level 110 and above.
const FeatureTryConvert = "tryConvert"

// Planner is the pure half of the SQL Server dialect: it diffs, lowers and
// guards without a database. The database half embeds it and overrides
// Capabilities, Inspect, Dependents (live), Open, Reconcile and Exec.
type Planner struct{}

var _ engine.Dialect = Planner{}

func needsDatabase(what string) error {
	return fmt.Errorf("sqlserver: %s requires the database dialect", what)
}

func (Planner) Name() string { return "sqlserver" }

// Capabilities answers only offline (q == nil): SQL Server DDL is transactional
// on every server, and none of the version- or compatibility-gated features
// (TRY_CONVERT among them) holds for all, so the planner keeps choosing the
// compatibility-level-100-safe forms.
func (Planner) Capabilities(_ context.Context, q engine.Querier) (engine.Capabilities, error) {
	if q == nil {
		return engine.Capabilities{TransactionalDDL: true}, nil
	}
	return engine.Capabilities{}, needsDatabase("reading capabilities")
}

func (Planner) Inspect(context.Context, engine.Querier, engine.Scope) (engine.Inspection, error) {
	return engine.Inspection{}, needsDatabase("inspection")
}

func (Planner) Open(context.Context, *sql.Conn, engine.SessionOptions) (engine.UnitSession, error) {
	return engine.UnitSession{}, needsDatabase("a session")
}

func (Planner) Reconcile(context.Context, engine.Querier, migrate.Schema, migrate.Schema) (migrate.Schema, error) {
	return migrate.Schema{}, needsDatabase("reconciling the desired schema with the live one")
}

// SameDependent is the strict comparison: SQL Server re-creates a dependent
// from the definition it recorded.
func (Planner) SameDependent(before, after engine.Dependent) (bool, error) {
	return engine.SameDefinition(before, after)
}

func (Planner) Exec(context.Context, engine.Querier, engine.Statement) error {
	return needsDatabase("running a statement")
}

// Diff compares two schemas, which may be narrowed to some objects: a foreign
// key may reference a table the schema does not hold.
func (Planner) Diff(current, desired migrate.Schema) (engine.Diff, error) {
	d, err := describeChanges(current, desired)
	if err != nil {
		return nil, err
	}
	p := &planDiff{described: d, current: current, desired: desired}
	if p.forcing, err = p.findForcing(); err != nil {
		return nil, err
	}
	return p, nil
}
