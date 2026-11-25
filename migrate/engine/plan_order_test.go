package engine_test

import (
	"errors"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Op ordering", func() {
	var f *fakeDialect

	BeforeEach(func() { f = newFake() })

	It("orders ops globally across tables: drop the child foreign key, alter parent and child, re-add the key", func() {
		child := change("modify", "column", "child", "parent_id")
		parent := change("modify", "column", "parent", "id")
		f.changes = []migrate.Change{child, parent}
		f.forcing = []engine.Forcing{
			{Change: child.ID, Columns: []engine.ColumnRef{col("child", "parent_id")}, Retype: true},
			{Change: parent.ID, Columns: []engine.ColumnRef{col("parent", "id")}, Retype: true},
		}
		f.offline = []engine.Dependent{foreignKey("child", "fk_child_parent", "parent_id", "parent", "id",
			col("child", "parent_id"), col("parent", "id"))}
		fk := changeID("rebuild", "foreign-key", "child", "fk_child_parent")

		a, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		Expect(opIDs(a)).To(Equal([]string{"drop:" + fk, "op:" + child.ID, "op:" + parent.ID, "create:" + fk}))
		Expect(a.Units).To(HaveLen(1))
		Expect(a.Units[0].Objects).To(Equal([]migrate.Name{tableName("child"), tableName("parent")}))
	})

	// Only the rebuild edges can order these: the cause ops share a phase with
	// the rebuild op they must precede or follow, and Lower lists it the wrong way round.
	DescribeTable("orders one rebuild with two causes on different tables around both causes",
		func(causePhase engine.Phase, want func(fk string, a, b migrate.Change) []string) {
			a, b := change("modify", "column", "parent", "id"), change("modify", "column", "child", "parent_id")
			f.changes = []migrate.Change{a, b}
			f.forcing = []engine.Forcing{
				{Change: a.ID, Columns: []engine.ColumnRef{col("parent", "id")}},
				{Change: b.ID, Columns: []engine.ColumnRef{col("child", "parent_id")}},
			}
			f.offline = []engine.Dependent{foreignKey("child", "fk_child_parent", "parent_id", "parent", "id",
				col("child", "parent_id"), col("parent", "id"))}
			fk := changeID("rebuild", "foreign-key", "child", "fk_child_parent")
			f.lower = func(selection []string, _ []engine.Rebuild) ([]engine.Op, error) {
				return []engine.Op{
					{ID: "create:" + fk, Change: fk, Phase: engine.PhaseCreate, Object: tableName("child")},
					{ID: "op:" + a.ID, Change: a.ID, Phase: causePhase, Object: a.Object},
					{ID: "op:" + b.ID, Change: b.ID, Phase: causePhase, Object: b.Object},
					{ID: "drop:" + fk, Change: fk, Phase: engine.PhaseDrop, Object: tableName("child")},
				}, nil
			}
			got, err := engine.Plan(f.request(engine.ScopeComplete))
			Expect(err).NotTo(HaveOccurred())
			Expect(opIDs(got)).To(Equal(want(fk, a, b)))
		},
		Entry("cause ops in the drop phase: the rebuild drop still comes first", engine.PhaseDrop,
			func(fk string, a, b migrate.Change) []string {
				return []string{"drop:" + fk, "op:" + a.ID, "op:" + b.ID, "create:" + fk}
			}),
		Entry("cause ops in the create phase: the rebuild create still comes last", engine.PhaseCreate,
			func(fk string, a, b migrate.Change) []string {
				return []string{"drop:" + fk, "op:" + a.ID, "op:" + b.ID, "create:" + fk}
			}),
	)

	It("breaks ties by phase, then by the order Lower returned", func() {
		add := change("add", "column", "orders", "note")
		modify := change("modify", "column", "orders", "id")
		drop := change("drop", "column", "orders", "old")
		f.changes = []migrate.Change{add, modify, drop}
		a, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		Expect(opIDs(a)).To(Equal([]string{"op:" + drop.ID, "op:" + modify.ID, "op:" + add.ID}))
	})

	It("honours explicit Requires over phase", func() {
		add := change("add", "column", "orders", "note")
		drop := change("drop", "column", "orders", "old")
		f.changes = []migrate.Change{add, drop}
		f.lower = func(selection []string, _ []engine.Rebuild) ([]engine.Op, error) {
			return []engine.Op{
				{ID: "late", Change: add.ID, Phase: engine.PhaseCreate, Object: add.Object, Statements: []engine.Statement{{SQL: "a"}}},
				{ID: "early", Change: drop.ID, Phase: engine.PhaseDrop, Object: drop.Object, Requires: []string{"late"}, Statements: []engine.Statement{{SQL: "b"}}},
			}, nil
		}
		a, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		Expect(opIDs(a)).To(Equal([]string{"late", "early"}))
	})

	It("fails on a dependency cycle, naming the ops", func() {
		x, y := change("add", "column", "orders", "x"), change("add", "column", "orders", "y")
		f.changes = []migrate.Change{x, y}
		f.lower = func([]string, []engine.Rebuild) ([]engine.Op, error) {
			return []engine.Op{
				{ID: "op-x", Change: x.ID, Phase: engine.PhaseCreate, Requires: []string{"op-y"}},
				{ID: "op-y", Change: y.ID, Phase: engine.PhaseCreate, Requires: []string{"op-x"}},
			}, nil
		}
		_, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).To(MatchError(And(ContainSubstring("cycle"), ContainSubstring("op-x"), ContainSubstring("op-y"))))
	})

	DescribeTable("rejects ops that do not match the plan",
		func(ops func(c migrate.Change) []engine.Op, want string) {
			c := change("add", "column", "orders", "note")
			f.changes = []migrate.Change{c}
			f.lower = func([]string, []engine.Rebuild) ([]engine.Op, error) { return ops(c), nil }
			_, err := engine.Plan(f.request(engine.ScopeComplete))
			Expect(err).To(MatchError(ContainSubstring(want)))
		},
		Entry("duplicate op ID", func(c migrate.Change) []engine.Op {
			return []engine.Op{{ID: "a", Change: c.ID, Phase: engine.PhaseCreate}, {ID: "a", Change: c.ID, Phase: engine.PhaseCreate}}
		}, "duplicate op ID a"),
		Entry("op of a change that is not planned", func(c migrate.Change) []engine.Op {
			return []engine.Op{{ID: "a", Change: c.ID, Phase: engine.PhaseCreate}, {ID: "b", Change: "stranger", Phase: engine.PhaseCreate}}
		}, "neither selected nor a rebuild"),
		Entry("Requires naming a missing op", func(c migrate.Change) []engine.Op {
			return []engine.Op{{ID: "a", Change: c.ID, Phase: engine.PhaseCreate, Requires: []string{"ghost"}}}
		}, `requires "ghost"`),
		Entry("unknown phase", func(c migrate.Change) []engine.Op {
			return []engine.Op{{ID: "a", Change: c.ID, Phase: "later"}}
		}, `phase "later"`),
		Entry("a planned change with no ops", func(c migrate.Change) []engine.Op { return nil }, "lowered to no ops"),
	)

	It("surfaces a Lower failure", func() {
		f.changes = []migrate.Change{change("add", "column", "orders", "note")}
		f.lower = func([]string, []engine.Rebuild) ([]engine.Op, error) { return nil, errors.New("no renderer") }
		_, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).To(MatchError(ContainSubstring("no renderer")))
	})
})
