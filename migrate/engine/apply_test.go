package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func worldSchema(world map[string]string) migrate.Schema {
	s := migrate.Schema{Tables: []migrate.Table{}}
	for _, name := range slices.Sorted(maps.Keys(world)) {
		table := migrate.Table{Name: tableName(name), Columns: []migrate.Column{{Name: "type", Type: world[name]}}}
		if _, ok := world["items"]; ok && name == "orders" {
			table.ForeignKeys = []migrate.ForeignKey{{
				Name: "fk_orders_items", Columns: []string{"item"}, References: tableName("items"), ReferencedColumns: []string{"id"},
			}}
		}
		s.Tables = append(s.Tables, table)
	}
	return s
}

// worldDiff is the executor tests' differ: a missing table is added, a table of
// another type is modified, and a table the desired schema lacks is dropped.
func worldDiff(current, desired migrate.Schema) []migrate.Change {
	types := func(s migrate.Schema) map[string]string {
		out := map[string]string{}
		for _, t := range s.Tables {
			out[t.Name.Name] = t.Columns[0].Type
		}
		return out
	}
	have, want := types(current), types(desired)
	var changes []migrate.Change
	for _, name := range slices.Sorted(maps.Keys(want)) {
		if _, ok := have[name]; !ok {
			changes = append(changes, change("add", "table", name, ""))
		} else if have[name] != want[name] {
			changes = append(changes, change("modify", "column", name, "type"))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(have)) {
		if _, ok := want[name]; !ok {
			changes = append(changes, change("drop", "table", name, ""))
		}
	}
	return changes
}

type executorFixture struct {
	server *fakeServer
	f      *fakeDialect
	verbs  map[string]string
	// indexCreate is the verb that re-creates a rebuilt index; "NOOP INDEX" leaves it missing.
	indexCreate string
	// native gives the planned snapshots an opaque Native description.
	native bool
	// nonTransactional lists the tables whose statements cannot run in a transaction.
	nonTransactional map[string]bool
}

func newExecutorFixture(world map[string]string) *executorFixture {
	x := &executorFixture{
		server: newFakeServer(), f: newFake(), nonTransactional: map[string]bool{}, indexCreate: "CREATE INDEX",
		verbs: map[string]string{"add": "CREATE", "modify": "ALTER", "drop": "DROP"},
	}
	x.server.world = maps.Clone(world)
	x.f.diffFn = worldDiff
	x.f.lower = x.lower
	x.f.inspect = x.inspect
	x.f.pre = func(c migrate.Change) []engine.Precondition {
		if c.Object.Name != "orders" || c.Action == "rebuild" {
			return nil
		}
		return []engine.Precondition{{ID: "orders-guard", Change: c.ID, SQL: "SELECT guard orders", Message: "orders holds rows that do not convert"}}
	}
	return x
}

// withIndex gives orders an index on its type column: changing the type forces
// the index to be dropped and re-created around the ALTER.
func (x *executorFixture) withIndex() {
	x.server.indexes["orders.ix_ref"] = "ref"
	forcing := []engine.ColumnRef{col("orders", "type")}
	x.f.forcing = []engine.Forcing{{Change: changeID("modify", "column", "orders", "type"), Columns: forcing}}
	x.f.dependentsFn = func(ctx context.Context, q engine.Querier, _ []engine.ColumnRef) []engine.Dependent {
		dep := dependent(engine.DependentIndex, "orders", "ix_ref", forcing...)
		dep.Index = &migrate.Index{Name: "ix_ref", Columns: []migrate.IndexColumn{{Name: "ref"}}}
		if q == nil {
			return []engine.Dependent{dep}
		}
		rows, err := q.QueryContext(ctx, "DEPENDENTS")
		Expect(err).NotTo(HaveOccurred())
		Expect(rows.Close()).To(Succeed())
		columns, ok := x.server.indexSnapshot()["orders.ix_ref"]
		if !ok {
			return nil
		}
		dep.Index.Columns[0].Name = columns
		return []engine.Dependent{dep}
	}
}

func (x *executorFixture) lowerRebuild(r engine.Rebuild) []engine.Op {
	key := r.Change.Object.Name + "." + r.Change.Member
	nonTx := x.nonTransactional[r.Change.Object.Name]
	drop := engine.Op{ID: "drop:" + r.Change.ID, Change: r.Change.ID, Phase: engine.PhaseDrop, Object: r.Change.Object,
		Statements: []engine.Statement{{SQL: "DROP INDEX " + key, NonTransactional: nonTx}}}
	create := engine.Op{ID: "create:" + r.Change.ID, Change: r.Change.ID, Phase: engine.PhaseCreate, Object: r.Change.Object,
		Requires: []string{drop.ID}, Statements: []engine.Statement{{SQL: x.indexCreate + " " + key, NonTransactional: nonTx}}}
	return []engine.Op{drop, create}
}

func (x *executorFixture) lower(selection []string, rebuilds []engine.Rebuild) ([]engine.Op, error) {
	var ops []engine.Op
	for _, r := range rebuilds {
		ops = append(ops, x.lowerRebuild(r)...)
	}
	for _, id := range selection {
		var tuple []string
		if err := json.Unmarshal([]byte(id), &tuple); err != nil {
			return nil, err
		}
		action, table := tuple[0], tuple[3]
		ops = append(ops, engine.Op{
			ID: "op:" + id, Change: id, Phase: phaseOf(action), Object: tableName(table),
			Statements: []engine.Statement{{SQL: x.verbs[action] + " " + table, NonTransactional: x.nonTransactional[table]}},
		})
	}
	return ops, nil
}

func (x *executorFixture) inspect(ctx context.Context, q engine.Querier, scope engine.Scope) (engine.Inspection, error) {
	rows, err := q.QueryContext(ctx, "INSPECT")
	if err != nil {
		return engine.Inspection{}, err
	}
	if err := rows.Close(); err != nil {
		return engine.Inspection{}, err
	}
	world := x.server.snapshot()
	if scope.Mode == engine.ScopeDeclared && len(scope.Objects) > 0 {
		for name := range world {
			if !slices.Contains(scope.Objects, tableName(name)) {
				delete(world, name)
			}
		}
	}
	return engine.Inspection{Schema: worldSchema(world)}, nil
}

func (x *executorFixture) plan(desired map[string]string) engine.Artifact {
	req := x.f.request(engine.ScopeComplete)
	req.Current, req.Desired = worldSchema(x.server.snapshot()), worldSchema(desired)
	if x.native {
		req.Current.Native, req.Desired.Native = "native:current", "native:desired"
	}
	a, err := engine.Plan(req)
	Expect(err).NotTo(HaveOccurred())
	return a
}

var _ = Describe("Apply", func() {
	var x *executorFixture
	var a engine.Artifact
	var request engine.ApplyRequest
	bigint := map[string]string{"items": "bigint", "orders": "bigint"}

	apply := func() (engine.Result, error) { return engine.Apply(context.Background(), x.server.open(), request) }
	session := func(u engine.Unit) string {
		return fmt.Sprintf("SESSION %s lock=%s", u.ID, request.LockTimeout)
	}

	BeforeEach(func() {
		x = newExecutorFixture(map[string]string{"items": "int", "orders": "int"})
		a = x.plan(bigint)
		request = engine.ApplyRequest{Artifact: a, Hash: a.Hash, LockTimeout: 5 * time.Second}
		Expect(a.Units).To(HaveLen(2))
	})

	It("runs each pending unit in its own transaction: session, preconditions, statements, commit", func() {
		result, err := apply()
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(engine.Result{Hash: a.Hash, Outcome: engine.Applied, Units: []engine.UnitResult{
			{ID: a.Units[0].ID, Outcome: engine.Applied, Statements: 1},
			{ID: a.Units[1].ID, Outcome: engine.Applied, Statements: 1},
		}}))
		Expect(x.server.snapshot()).To(Equal(bigint))
		Expect(x.server.logged("INSPECT")).To(Equal([]string{
			"BEGIN", session(a.Units[0]), "ALTER items", "COMMIT",
			"BEGIN", session(a.Units[1]), "SELECT guard orders", "ALTER orders", "COMMIT",
		}))
	})

	It("inspects only the unit's own objects", func() {
		_, err := apply()
		Expect(err).NotTo(HaveOccurred())
		for _, scope := range x.f.scopes {
			Expect(scope.Mode).To(Equal(engine.ScopeDeclared))
			Expect(scope.Schemas).To(Equal([]string{"dbo"}))
			Expect(scope.Objects).To(HaveLen(1))
		}
	})

	It("skips units already at their desired state and rolls back the read-only transaction", func() {
		x.server.world = maps.Clone(bigint)
		result, err := apply()
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Outcome).To(Equal(engine.AlreadyApplied))
		Expect(x.server.logged("INSPECT")).To(Equal([]string{
			"BEGIN", session(a.Units[0]), "ROLLBACK",
			"BEGIN", session(a.Units[1]), "ROLLBACK",
		}))
	})

	It("applies only the pending unit when another is already done", func() {
		x.server.world["items"] = "bigint"
		result, err := apply()
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Outcome).To(Equal(engine.Applied))
		Expect([]engine.Outcome{result.Units[0].Outcome, result.Units[1].Outcome}).To(Equal([]engine.Outcome{engine.AlreadyApplied, engine.Applied}))
	})

	It("stops on drift before running any statement of the unit", func() {
		x.server.world["items"] = "text"
		result, err := apply()
		var drift *engine.DriftError
		Expect(errors.As(err, &drift)).To(BeTrue())
		Expect(drift.Unit).To(Equal(a.Units[0].ID))
		Expect(result.Outcome).To(Equal(engine.Failed))
		Expect(x.server.logged("INSPECT", "BEGIN", "ROLLBACK", "SESSION")).To(BeEmpty())
	})

	It("rolls back a unit whose precondition returns a row and leaves earlier units committed", func() {
		x.server.rows["SELECT guard orders"] = true
		result, err := apply()
		var pre *engine.PreconditionError
		Expect(errors.As(err, &pre)).To(BeTrue())
		Expect(pre).To(Equal(&engine.PreconditionError{Unit: a.Units[1].ID, ID: "orders-guard", Message: "orders holds rows that do not convert"}))
		Expect(result.Outcome).To(Equal(engine.Failed))
		Expect(x.server.snapshot()).To(Equal(map[string]string{"items": "bigint", "orders": "int"}))
		Expect(x.server.logged("INSPECT")).NotTo(ContainElement("ALTER orders"))
	})

	It("keeps earlier units committed when a later statement fails, and resumes on re-run", func() {
		cause := errors.New("DDL denied")
		x.server.execErr["ALTER orders"] = cause
		result, err := apply()
		Expect(errors.Is(err, cause)).To(BeTrue())
		Expect(result.Outcome).To(Equal(engine.Failed))
		Expect(result.Units).To(Equal([]engine.UnitResult{
			{ID: a.Units[0].ID, Outcome: engine.Applied, Statements: 1},
			{ID: a.Units[1].ID, Outcome: engine.Failed},
		}))
		Expect(x.server.snapshot()).To(Equal(map[string]string{"items": "bigint", "orders": "int"}))

		delete(x.server.execErr, "ALTER orders")
		result, err = apply()
		Expect(err).NotTo(HaveOccurred())
		Expect([]engine.Outcome{result.Units[0].Outcome, result.Units[1].Outcome}).To(Equal([]engine.Outcome{engine.AlreadyApplied, engine.Applied}))
		Expect(x.server.snapshot()).To(Equal(bigint))
	})

	It("fails the postcondition when the statements did not produce the desired state", func() {
		x.verbs["modify"] = "NOOP"
		a = x.plan(bigint)
		request.Artifact, request.Hash = a, a.Hash
		result, err := apply()
		Expect(err).To(MatchError(ContainSubstring("postcondition")))
		Expect(result.Outcome).To(Equal(engine.Failed))
		Expect(x.server.snapshot()).To(Equal(map[string]string{"items": "int", "orders": "int"}))
	})

	It("reports an uncertain commit separately", func() {
		cause := errors.New("connection lost while committing")
		x.server.commitErr = cause
		result, err := apply()
		Expect(errors.Is(err, cause)).To(BeTrue())
		Expect(result.Outcome).To(Equal(engine.CommitUnknown))
		Expect(result.Units).To(HaveLen(1))
	})

	It("runs a unit with a non-transactional statement on a dedicated connection, without BEGIN or COMMIT", func() {
		x.nonTransactional["items"] = true
		a = x.plan(bigint)
		request.Artifact, request.Hash = a, a.Hash
		Expect(a.Units[0].Transactional).To(BeFalse())
		_, err := apply()
		Expect(err).NotTo(HaveOccurred())
		Expect(x.server.logged("INSPECT")[:2]).To(Equal([]string{session(a.Units[0]), "ALTER items"}))
		Expect(x.server.logged("INSPECT")[2]).To(Equal("BEGIN"))
	})

	It("rejects a changed reviewed hash before touching the database", func() {
		request.Hash = "changed"
		_, err := apply()
		Expect(err).To(MatchError(ContainSubstring("reviewed hash")))
		Expect(x.server.logged()).To(BeEmpty())
	})

	It("rejects a tampered artifact before touching the database", func() {
		request.Artifact.Units[0].Ops[0].Statements[0].SQL = "DROP DATABASE prod"
		_, err := apply()
		Expect(err).To(MatchError(ContainSubstring("hash mismatch")))
		Expect(x.server.logged()).To(BeEmpty())
	})

	It("rejects a negative lock timeout", func() {
		request.LockTimeout = -time.Second
		_, err := apply()
		Expect(err).To(MatchError(ContainSubstring("lock timeout")))
	})

	Describe("destructive changes", func() {
		BeforeEach(func() {
			a = x.plan(map[string]string{"orders": "int"})
			request = engine.ApplyRequest{Artifact: a, Hash: a.Hash}
			Expect(engine.Acknowledgements(a)).To(HaveLen(1))
		})

		It("requires each destructive change ID exactly once and rejects unknown or repeated ones", func() {
			id := engine.Acknowledgements(a)[0]
			for _, acks := range [][]string{nil, {"unknown"}, {id, id}} {
				request.Acknowledgements = acks
				_, err := apply()
				Expect(err).To(MatchError(ContainSubstring("acknowledg")))
			}
			Expect(x.server.logged()).To(BeEmpty())
		})

		It("applies once acknowledged", func() {
			request.Acknowledgements = engine.Acknowledgements(a)
			result, err := apply()
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Outcome).To(Equal(engine.Applied))
			Expect(x.server.snapshot()).To(Equal(map[string]string{"orders": "int"}))
		})
	})
})

var _ = Describe("Check", func() {
	var x *executorFixture
	var a engine.Artifact

	BeforeEach(func() {
		x = newExecutorFixture(map[string]string{"items": "int", "orders": "int"})
		a = x.plan(map[string]string{"items": "bigint", "orders": "bigint"})
	})

	It("answers applied or pending per unit", func() {
		x.server.world["items"] = "bigint"
		states, err := engine.Check(context.Background(), x.server.open(), a)
		Expect(err).NotTo(HaveOccurred())
		Expect(states).To(Equal(map[string]engine.UnitState{a.Units[0].ID: engine.UnitApplied, a.Units[1].ID: engine.UnitPending}))
	})

	It("reports a unit that is neither at baseline nor desired as drift", func() {
		x.server.world["orders"] = "text"
		_, err := engine.Check(context.Background(), x.server.open(), a)
		var drift *engine.DriftError
		Expect(errors.As(err, &drift)).To(BeTrue())
		Expect(drift.Unit).To(Equal(a.Units[1].ID))
		Expect(changeIDs(drift.Changes)).To(Equal([]string{changeID("modify", "column", "orders", "type")}))
	})
})
