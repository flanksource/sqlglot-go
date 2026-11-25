// Package engine is the dialect-neutral schema planner and executor. A Dialect
// supplies inspection, diffing and lowering; the engine adds scope policy,
// forced rebuilds of dependent objects, global ordering, unit grouping, the
// reviewed artifact and its executor once, for every dialect.
package engine

import (
	"context"
	"database/sql"
	"time"

	"github.com/flanksource/sqlglot-go/migrate"
)

// Querier is satisfied by *sql.DB, *sql.Conn and *sql.Tx.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type ScopeMode string

const (
	// ScopeDeclared manages only the named objects: an undeclared table or other
	// object is never dropped. A declared table is complete unless it is marked
	// partial, so its undeclared members are dropped.
	ScopeDeclared ScopeMode = "declared"
	// ScopeComplete manages every object of the schemas; drops are planned,
	// except for the members of a partial table.
	ScopeComplete ScopeMode = "complete"
)

type Scope struct {
	Mode    ScopeMode      `json:"mode"`
	Schemas []string       `json:"schemas,omitempty"`
	Objects []migrate.Name `json:"objects,omitempty"` // the managed objects in declared mode
}

// DroppedKind says what a forcing change removes.
type DroppedKind string

const (
	DroppedColumn DroppedKind = "column"
	DroppedTable  DroppedKind = "table"
)

type ColumnRef struct {
	Table  migrate.Name `json:"table"`
	Column string       `json:"column"`
	// Dropped is set when the forcing change removes this column, or its whole table (an empty Column names the table itself), so a dependent on it cannot be re-created.
	Dropped DroppedKind `json:"dropped,omitempty"`
}

type DependentKind string

const (
	DependentIndex            DependentKind = "index"
	DependentPrimaryKey       DependentKind = "primary-key"
	DependentUniqueConstraint DependentKind = "unique-constraint"
	DependentForeignKey       DependentKind = "foreign-key"
	DependentCheck            DependentKind = "check"
	DependentDefault          DependentKind = "default"
	DependentStatistic        DependentKind = "statistic"
	DependentView             DependentKind = "view"
	DependentTable            DependentKind = "table"
)

// Dependent is an object that depends on columns and must be dropped and
// re-created when they change. For the view and table kinds the dependent is
// the object itself: Table names it and its rebuild change has no member.
type Dependent struct {
	Kind       DependentKind       `json:"kind"`
	Table      migrate.Name        `json:"table"`
	Name       string              `json:"name"`
	On         []ColumnRef         `json:"on,omitempty"` // the forcing columns it depends on
	Index      *migrate.Index      `json:"index,omitempty"`
	ForeignKey *migrate.ForeignKey `json:"foreignKey,omitempty"`
	Check      *migrate.Check      `json:"check,omitempty"`
	// Definition is the live definition for kinds with no model type (statistic, view).
	Definition string `json:"definition,omitempty"`
	// Refusal, when set, says why the dependent cannot be reproduced faithfully.
	Refusal string `json:"refusal,omitempty"`
}

// Forcing is a change that forces dependents on its columns to be rebuilt.
type Forcing struct {
	Change  string      `json:"change"`
	Columns []ColumnRef `json:"columns"`
	// Retype means foreign-key partner columns must change with it.
	Retype bool `json:"retype,omitempty"`
}

// Rebuild is a forced drop and re-create of a dependent. Change.Action is
// "rebuild" and Change.Cause names the forcing change.
type Rebuild struct {
	Change    migrate.Change `json:"change"`
	Dependent Dependent      `json:"dependent"`
}

type Phase string

const (
	PhaseDrop   Phase = "drop"
	PhaseAlter  Phase = "alter"
	PhaseCreate Phase = "create"
)

type Statement struct {
	SQL              string `json:"sql"`
	NonTransactional bool   `json:"nonTransactional,omitempty"`
	SizeOfData       bool   `json:"sizeOfData,omitempty"`
}

// Op is the unit of ordering: the statements one change contributes in one phase.
type Op struct {
	ID         string       `json:"id"`
	Change     string       `json:"change"`
	Phase      Phase        `json:"phase"`
	Object     migrate.Name `json:"object"`
	Statements []Statement  `json:"statements"`
	Requires   []string     `json:"requires,omitempty"`
}

// Precondition passes when its SQL returns no rows.
type Precondition struct {
	ID      string `json:"id"`
	Change  string `json:"change"`
	SQL     string `json:"sql"`
	Message string `json:"message"`
}

// Unit is a connected component of the op graph, executed in one transaction
// where the dialect allows it.
type Unit struct {
	ID            string         `json:"id"`
	Changes       []string       `json:"changes"`
	Objects       []migrate.Name `json:"objects"`
	Ops           []Op           `json:"ops"`
	Preconditions []Precondition `json:"preconditions,omitempty"`
	Transactional bool           `json:"transactional"`
}

type Capabilities struct {
	Fingerprint      string          `json:"fingerprint"`
	TransactionalDDL bool            `json:"transactionalDdl"`
	Features         map[string]bool `json:"features,omitempty"`
}

// Refusal is an object or member the inspector could not read faithfully.
type Refusal struct {
	Object migrate.Name `json:"object"`
	Member string       `json:"member,omitempty"`
	Reason string       `json:"reason"`
}

type Inspection struct {
	Schema   migrate.Schema `json:"schema"`
	Refusals []Refusal      `json:"refusals,omitempty"`
}

type SessionOptions struct {
	LockTimeout time.Duration
	Unit        string
	// Transactional is the unit's flag: its statements may share one transaction.
	Transactional bool
}

// UnitSession is how a dialect runs one unit on its dedicated connection.
type UnitSession struct {
	// Q runs the unit's inspection, preconditions and statements.
	Q Querier
	// Finish commits or rolls back, releases locks and restores the
	// connection's settings. The engine calls it exactly once.
	Finish func(ctx context.Context, commit bool) error
}
