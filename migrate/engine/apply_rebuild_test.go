package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Rebuilt dependents", func() {
	var x *executorFixture
	var a engine.Artifact
	var request engine.ApplyRequest
	intOrders := map[string]string{"orders": "int"}
	bigintOrders := map[string]string{"orders": "bigint"}

	apply := func() (engine.Result, error) { return engine.Apply(context.Background(), x.server.open(), request) }
	plan := func() {
		a = x.plan(bigintOrders)
		request = engine.ApplyRequest{Artifact: a, Hash: a.Hash}
		Expect(a.Units).To(HaveLen(1))
		Expect(a.Dependents).To(HaveLen(1))
	}
	check := func() error {
		_, err := engine.Check(context.Background(), x.server.open(), a)
		return err
	}

	BeforeEach(func() {
		x = newExecutorFixture(intOrders)
		x.withIndex()
	})

	It("drops the index, alters the table and re-creates the index in one transaction", func() {
		plan()
		result, err := apply()
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Outcome).To(Equal(engine.Applied))
		Expect(x.server.logged("INSPECT", "DEPENDENTS", "SESSION")).To(Equal([]string{
			"BEGIN", "SELECT guard orders", "DROP INDEX orders.ix_ref", "ALTER orders", "CREATE INDEX orders.ix_ref", "COMMIT",
		}))
		Expect(x.server.indexSnapshot()).To(Equal(map[string]string{"orders.ix_ref": "ref"}))
	})

	It("fails the postcondition with a PartialError and rolls back when the create statement is a no-op", func() {
		x.indexCreate = "NOOP INDEX"
		plan()
		_, err := apply()
		var partial *engine.PartialError
		Expect(errors.As(err, &partial)).To(BeTrue())
		Expect(partial.Unit).To(Equal(a.Units[0].ID))
		Expect(partial.Missing).To(HaveLen(1))
		Expect(partial.Missing[0].Name).To(Equal("ix_ref"))
		Expect(err).To(MatchError(And(ContainSubstring("postcondition"), ContainSubstring("orders.ix_ref dropped by this unit and not re-created"))))
		Expect(x.server.indexSnapshot()).To(HaveKey("orders.ix_ref"))
		Expect(x.server.snapshot()).To(Equal(intOrders))
	})

	It("reports a PartialError, never applied or pending, when a non-transactional unit was interrupted between ALTER and CREATE", func() {
		x.nonTransactional["orders"] = true
		plan()
		Expect(a.Units[0].Transactional).To(BeFalse())
		cause := errors.New("connection reset")
		x.server.execErr["CREATE INDEX orders.ix_ref"] = cause
		_, err := apply()
		Expect(errors.Is(err, cause)).To(BeTrue())
		Expect(x.server.snapshot()).To(Equal(bigintOrders))
		Expect(x.server.indexSnapshot()).To(BeEmpty())

		delete(x.server.execErr, "CREATE INDEX orders.ix_ref")
		result, err := apply()
		var partial *engine.PartialError
		Expect(errors.As(err, &partial)).To(BeTrue())
		Expect(partial.Unit).To(Equal(a.Units[0].ID))
		Expect(result.Outcome).To(Equal(engine.Failed))
		Expect(slices.Index(x.server.logged("INSPECT"), "ALTER orders")).To(BeNumerically(">=", 0))
		Expect(x.server.indexSnapshot()).To(BeEmpty())
		Expect(errors.As(check(), &partial)).To(BeTrue())
	})

	It("reports a PartialError when the dependent is back but with a different definition", func() {
		plan()
		x.server.world["orders"] = "bigint"
		x.server.indexes["orders.ix_ref"] = "other"
		var partial *engine.PartialError
		Expect(errors.As(check(), &partial)).To(BeTrue())
		Expect(partial.Missing[0].Name).To(Equal("ix_ref"))
	})

	It("classifies a unit applied once its own changes are gone and its dependent is back as before", func() {
		plan()
		x.server.world["orders"] = "bigint"
		states, err := engine.Check(context.Background(), x.server.open(), a)
		Expect(err).NotTo(HaveOccurred())
		Expect(states).To(Equal(map[string]engine.UnitState{a.Units[0].ID: engine.UnitApplied}))
	})
})

var _ = Describe("Unit-narrowed classification", func() {
	var x *executorFixture
	var a engine.Artifact

	BeforeEach(func() {
		x = newExecutorFixture(map[string]string{"items": "int", "orders": "int"})
		x.native = true
		a = x.plan(map[string]string{"items": "int", "orders": "bigint"})
		Expect(a.Units).To(HaveLen(1))
		x.f.diffCalls = nil
	})

	// unitDiffs checks the artifact and returns the schema pairs classification
	// diffed: to desired, then to baseline. Check's own re-plan diffs first.
	unitDiffs := func() (map[string]engine.UnitState, [][2]migrate.Schema) {
		states, err := engine.Check(context.Background(), x.server.open(), a)
		Expect(err).NotTo(HaveOccurred())
		return states, x.f.diffCalls[1:]
	}

	It("diffs only the unit's objects on both sides, so a foreign key to an outside table reports nothing for it", func() {
		states, calls := unitDiffs()
		Expect(states).To(Equal(map[string]engine.UnitState{a.Units[0].ID: engine.UnitPending}))
		Expect(calls).To(HaveLen(2))
		for _, call := range calls {
			for _, side := range call {
				Expect(side.Tables).To(HaveLen(1))
				Expect(side.Tables[0].Name).To(Equal(tableName("orders")))
			}
		}
		Expect(calls[0][1].Tables[0].ForeignKeys[0].References).To(Equal(tableName("items")))
	})

	It("passes Native through canonicalisation, narrowing and a JSON round trip, and hashes it", func() {
		Expect(a.Baseline.Native).To(Equal("native:current"))
		Expect(a.Desired.Native).To(Equal("native:desired"))
		_, calls := unitDiffs()
		Expect(calls[0][1].Native).To(Equal("native:desired"))
		Expect(calls[1][1].Native).To(Equal("native:current"))

		body, err := json.Marshal(a)
		Expect(err).NotTo(HaveOccurred())
		var decoded engine.Artifact
		Expect(json.Unmarshal(body, &decoded)).To(Succeed())
		Expect(engine.Validate(decoded)).To(Succeed())

		decoded.Desired.Native = "native:other"
		Expect(engine.Validate(decoded)).To(MatchError(ContainSubstring("hash mismatch")))
	})
})

var _ = Describe("Statement execution and capabilities", func() {
	var x *executorFixture
	var a engine.Artifact
	var request engine.ApplyRequest

	apply := func() (engine.Result, error) { return engine.Apply(context.Background(), x.server.open(), request) }
	plan := func() {
		a = x.plan(map[string]string{"items": "bigint", "orders": "bigint"})
		request = engine.ApplyRequest{Artifact: a, Hash: a.Hash}
	}

	BeforeEach(func() { x = newExecutorFixture(map[string]string{"items": "int", "orders": "int"}) })

	It("routes every statement through the dialect's Exec, in plan order", func() {
		plan()
		_, err := apply()
		Expect(err).NotTo(HaveOccurred())
		var want []string
		for _, u := range a.Units {
			for _, op := range u.Ops {
				for _, s := range op.Statements {
					want = append(want, s.SQL)
				}
			}
		}
		Expect(want).To(Equal([]string{"ALTER items", "ALTER orders"}))
		Expect(x.f.executed).To(Equal(want))
	})

	It("refuses an artifact planned against a different capability fingerprint, before any unit runs", func() {
		plan()
		x.f.caps.Fingerprint = "fake-2"
		_, err := apply()
		Expect(err).To(MatchError(And(ContainSubstring("planned against"), ContainSubstring("fake-1"), ContainSubstring("fake-2"), ContainSubstring("re-plan"))))
		Expect(x.server.logged()).To(BeEmpty())
	})

	It("skips the capability check for an artifact planned offline, which records no fingerprint", func() {
		x.f.caps.Fingerprint = ""
		plan()
		x.f.caps.Fingerprint = "fake-2"
		result, err := apply()
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Outcome).To(Equal(engine.Applied))
	})
})

var _ = Describe("Unit objects", func() {
	It("include a rebuild's table even when it differs from the cause's table", func() {
		f := newFake()
		cause := change("modify", "column", "parent", "id")
		f.changes = []migrate.Change{cause}
		f.forcing = []engine.Forcing{{Change: cause.ID, Columns: []engine.ColumnRef{col("parent", "id")}}}
		f.offline = []engine.Dependent{foreignKey("child", "fk_child_parent", "parent_id", "parent", "id", col("parent", "id"))}
		a, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		Expect(a.Units).To(HaveLen(1))
		Expect(a.Units[0].Objects).To(Equal([]migrate.Name{tableName("child"), tableName("parent")}))
	})
})
