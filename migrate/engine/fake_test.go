package engine_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

var dialectCounter atomic.Int64

type lowerFunc func(selection []string, rebuilds []engine.Rebuild) ([]engine.Op, error)

type dependentCall struct {
	offline bool
	columns []engine.ColumnRef
}

// fakeDialect is a scripted dialect: the tests set the diff, forcings,
// dependents and ops directly, so every planner rule is exercised without a database.
type fakeDialect struct {
	name    string
	changes []migrate.Change
	forcing []engine.Forcing
	offline []engine.Dependent
	live    []engine.Dependent
	caps    engine.Capabilities
	// offlineCaps is what the dialect answers when asked with no database.
	offlineCaps engine.Capabilities
	diffFn      func(current, desired migrate.Schema) []migrate.Change
	lower       lowerFunc
	pre         func(migrate.Change) []engine.Precondition
	inspect     func(ctx context.Context, q engine.Querier, scope engine.Scope) (engine.Inspection, error)
	scopes      []engine.Scope
	calls       []dependentCall
	// warnings are what every diff reports.
	warnings []string
	// dependentsFn, when set, answers Dependents for both the offline and live reads.
	dependentsFn func(ctx context.Context, q engine.Querier, columns []engine.ColumnRef) []engine.Dependent
	// diffCalls records every schema pair the engine diffed.
	diffCalls [][2]migrate.Schema
	// executed records every statement the engine routed through Exec.
	executed []string
	// opened and finished record every session the engine opened and finished.
	opened   []engine.SessionOptions
	finished []finishCall
	// commitFails and rollbackFails make Finish fail (after rolling back).
	commitFails, rollbackFails error
	// reconcile, when set, rewrites the desired schema; reconciled records every call.
	reconcile  func(current, desired migrate.Schema) migrate.Schema
	reconciled [][2]migrate.Schema
	// same, when set, replaces the strict dependent comparison.
	same func(before, after engine.Dependent) bool
}

func newFake() *fakeDialect {
	f := &fakeDialect{
		name: fmt.Sprintf("fake%d", dialectCounter.Add(1)),
		caps: engine.Capabilities{Fingerprint: "fake-1", TransactionalDDL: true},
	}
	f.lower = f.defaultLower
	engine.Register(f)
	return f
}

func (f *fakeDialect) Name() string { return f.name }

// Capabilities answers the scripted live capabilities, or offlineCaps for q == nil.
func (f *fakeDialect) Capabilities(_ context.Context, q engine.Querier) (engine.Capabilities, error) {
	if q == nil {
		return f.offlineCaps, nil
	}
	return f.caps, nil
}

func (f *fakeDialect) Inspect(ctx context.Context, q engine.Querier, scope engine.Scope) (engine.Inspection, error) {
	f.scopes = append(f.scopes, scope)
	if f.inspect == nil {
		return engine.Inspection{}, nil
	}
	return f.inspect(ctx, q, scope)
}

func (f *fakeDialect) Dependents(ctx context.Context, q engine.Querier, _ migrate.Schema, columns []engine.ColumnRef) ([]engine.Dependent, error) {
	f.calls = append(f.calls, dependentCall{offline: q == nil, columns: columns})
	if f.dependentsFn != nil {
		return f.dependentsFn(ctx, q, columns), nil
	}
	if q == nil {
		return slices.Clone(f.offline), nil
	}
	return slices.Clone(f.live), nil
}

func (f *fakeDialect) Exec(ctx context.Context, q engine.Querier, s engine.Statement) error {
	f.executed = append(f.executed, s.SQL)
	_, err := q.ExecContext(ctx, s.SQL)
	return err
}

func (f *fakeDialect) Diff(current, desired migrate.Schema) (engine.Diff, error) {
	f.diffCalls = append(f.diffCalls, [2]migrate.Schema{current, desired})
	changes := f.changes
	if f.diffFn != nil {
		changes = f.diffFn(current, desired)
	}
	return &fakeDiff{f: f, changes: changes}, nil
}

func (f *fakeDialect) Preconditions(c migrate.Change, _, _ migrate.Schema, _ engine.Capabilities) ([]engine.Precondition, error) {
	if f.pre == nil {
		return nil, nil
	}
	return f.pre(c), nil
}

// finishCall is one Finish the engine made.
type finishCall struct {
	Unit   string
	Commit bool
}

// Open begins a transaction for a transactional unit (TxSession) or uses the
// connection as is, runs the SESSION statement, and records every Finish.
func (f *fakeDialect) Open(ctx context.Context, conn *sql.Conn, opts engine.SessionOptions) (engine.UnitSession, error) {
	f.opened = append(f.opened, opts)
	session := func(ctx context.Context, q engine.Querier) error {
		_, err := q.ExecContext(ctx, fmt.Sprintf("SESSION %s lock=%s", opts.Unit, opts.LockTimeout))
		return err
	}
	var s engine.UnitSession
	var err error
	if opts.Transactional {
		s, err = engine.TxSession(ctx, conn, session)
	} else {
		s, err = engine.UnitSession{Q: conn, Finish: func(context.Context, bool) error { return nil }}, session(ctx, conn)
	}
	if err != nil {
		return engine.UnitSession{}, err
	}
	finish := s.Finish
	s.Finish = func(ctx context.Context, commit bool) error {
		f.finished = append(f.finished, finishCall{Unit: opts.Unit, Commit: commit})
		if commit && f.commitFails != nil {
			return errors.Join(f.commitFails, finish(ctx, false))
		}
		if !commit && f.rollbackFails != nil {
			return errors.Join(f.rollbackFails, finish(ctx, false))
		}
		return finish(ctx, commit)
	}
	return s, nil
}

func (f *fakeDialect) Reconcile(_ context.Context, _ engine.Querier, current, desired migrate.Schema) (migrate.Schema, error) {
	f.reconciled = append(f.reconciled, [2]migrate.Schema{current, desired})
	if f.reconcile == nil {
		return desired, nil
	}
	return f.reconcile(current, desired), nil
}

func (f *fakeDialect) SameDependent(before, after engine.Dependent) (bool, error) {
	if f.same != nil {
		return f.same(before, after), nil
	}
	return engine.SameDefinition(before, after)
}

// defaultLower gives each change one op in the phase its action implies, and
// each rebuild a drop op and a create op that follows it.
func (f *fakeDialect) defaultLower(selection []string, rebuilds []engine.Rebuild) ([]engine.Op, error) {
	var ops []engine.Op
	for _, id := range selection {
		i := slices.IndexFunc(f.changes, func(c migrate.Change) bool { return c.ID == id })
		if i < 0 {
			return nil, fmt.Errorf("fake: no change %s", id)
		}
		c := f.changes[i]
		ops = append(ops, engine.Op{
			ID: "op:" + id, Change: id, Phase: phaseOf(c.Action), Object: c.Object,
			Statements: []engine.Statement{{SQL: c.Action + " " + c.Kind + " " + c.Member}},
		})
	}
	for _, r := range rebuilds {
		drop := engine.Op{ID: "drop:" + r.Change.ID, Change: r.Change.ID, Phase: engine.PhaseDrop, Object: r.Change.Object,
			Statements: []engine.Statement{{SQL: "DROP " + r.Change.Member}}}
		create := engine.Op{ID: "create:" + r.Change.ID, Change: r.Change.ID, Phase: engine.PhaseCreate, Object: r.Change.Object,
			Requires: []string{drop.ID}, Statements: []engine.Statement{{SQL: "CREATE " + r.Change.Member}}}
		ops = append(ops, drop, create)
	}
	return ops, nil
}

func phaseOf(action string) engine.Phase {
	switch action {
	case "drop":
		return engine.PhaseDrop
	case "add":
		return engine.PhaseCreate
	}
	return engine.PhaseAlter
}

type fakeDiff struct {
	f       *fakeDialect
	changes []migrate.Change
}

func (d *fakeDiff) Changes() []migrate.Change { return slices.Clone(d.changes) }
func (d *fakeDiff) Forcing() []engine.Forcing { return slices.Clone(d.f.forcing) }
func (d *fakeDiff) Warnings() []string        { return slices.Clone(d.f.warnings) }
func (d *fakeDiff) Lower(selection []string, rebuilds []engine.Rebuild) ([]engine.Op, error) {
	return d.f.lower(selection, rebuilds)
}

func tableName(table string) migrate.Name { return migrate.Name{Schema: "dbo", Name: table} }

func changeID(action, kind, table, member string) string {
	id, err := json.Marshal([]string{action, kind, "dbo", table, member})
	if err != nil {
		panic(err)
	}
	return string(id)
}

func change(action, kind, table, member string, requires ...string) migrate.Change {
	return migrate.Change{
		ID: changeID(action, kind, table, member), Action: action, Kind: kind, Object: tableName(table), Member: member,
		Destructive: action == "drop", Requires: requires,
	}
}

func col(table, column string) engine.ColumnRef {
	return engine.ColumnRef{Table: tableName(table), Column: column}
}

func dependent(kind engine.DependentKind, table, name string, on ...engine.ColumnRef) engine.Dependent {
	return engine.Dependent{Kind: kind, Table: tableName(table), Name: name, On: on}
}

func foreignKey(table, name, column, refTable, refColumn string, on ...engine.ColumnRef) engine.Dependent {
	d := dependent(engine.DependentForeignKey, table, name, on...)
	d.ForeignKey = &migrate.ForeignKey{Name: name, Columns: []string{column}, References: tableName(refTable), ReferencedColumns: []string{refColumn}}
	return d
}

// request plans in the given mode; a declared scope manages the orders table.
func (f *fakeDialect) request(mode engine.ScopeMode, selection ...string) engine.PlanRequest {
	scope := engine.Scope{Mode: mode, Schemas: []string{"dbo"}}
	if mode == engine.ScopeDeclared {
		scope.Objects = []migrate.Name{tableName("orders")}
	}
	return engine.PlanRequest{Dialect: f.name, Scope: scope, Selection: selection, Capabilities: f.caps}
}

func changeIDs(changes []migrate.Change) []string {
	out := make([]string, len(changes))
	for i, c := range changes {
		out[i] = c.ID
	}
	return out
}

func opIDs(a engine.Artifact) []string {
	var out []string
	for _, u := range a.Units {
		for _, op := range u.Ops {
			out = append(out, op.ID)
		}
	}
	return out
}
