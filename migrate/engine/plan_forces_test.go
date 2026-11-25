package engine_test

import (
	"slices"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Forced rebuilds", func() {
	var f *fakeDialect
	refChange := change("modify", "column", "orders", "ref")
	indexRebuild := changeID("rebuild", "index", "orders", "ix_ref")

	BeforeEach(func() {
		f = newFake()
		f.changes = []migrate.Change{refChange}
		f.forcing = []engine.Forcing{{Change: refChange.ID, Columns: []engine.ColumnRef{col("orders", "ref")}}}
	})

	rebuildOf := func(a engine.Artifact, id string) migrate.Change {
		i := slices.IndexFunc(a.Changes, func(c migrate.Change) bool { return c.ID == id })
		Expect(i).To(BeNumerically(">=", 0), "rebuild %s not planned", id)
		return a.Changes[i]
	}

	It("creates a rebuild change for a dependent on the forced column, with its cause", func() {
		f.offline = []engine.Dependent{dependent(engine.DependentIndex, "orders", "ix_ref", col("orders", "ref"))}
		a, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		Expect(rebuildOf(a, indexRebuild)).To(Equal(migrate.Change{
			ID: indexRebuild, Action: "rebuild", Kind: "index", Object: tableName("orders"), Member: "ix_ref",
			Reason:   rebuildOf(a, indexRebuild).Reason,
			Requires: []string{refChange.ID}, Cause: refChange.ID,
		}))
		Expect(opIDs(a)).To(ConsistOf("op:"+refChange.ID, "drop:"+indexRebuild, "create:"+indexRebuild))
	})

	It("de-duplicates a dependent supplied by the caller and derived offline", func() {
		f.offline = []engine.Dependent{dependent(engine.DependentIndex, "orders", "ix_ref", col("orders", "ref"))}
		req := f.request(engine.ScopeComplete)
		req.Dependents = []engine.Dependent{dependent(engine.DependentIndex, "orders", "ix_ref", col("orders", "ref"))}
		a, err := engine.Plan(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(a.Changes).To(HaveLen(2))
		Expect(opIDs(a)).To(HaveLen(3))
	})

	It("rebuilds a dependent once with every forcing change it shares as requirements, the first sorted as its cause", func() {
		other := change("modify", "column", "orders", "ref2")
		f.changes = append(f.changes, other)
		f.forcing = append(f.forcing, engine.Forcing{Change: other.ID, Columns: []engine.ColumnRef{col("orders", "ref2")}})
		f.offline = []engine.Dependent{dependent(engine.DependentIndex, "orders", "ix_ref", col("orders", "ref"), col("orders", "ref2"))}
		a, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		rebuild := rebuildOf(a, indexRebuild)
		sorted := []string{refChange.ID, other.ID}
		slices.Sort(sorted)
		Expect(rebuild.Requires).To(Equal(sorted))
		Expect(rebuild.Cause).To(Equal(sorted[0]))
		Expect(a.Changes).To(HaveLen(3))
	})

	It("skips a dependent a selected change already targets", func() {
		f.changes = append(f.changes, change("modify", "index", "orders", "ix_ref"))
		f.offline = []engine.Dependent{dependent(engine.DependentIndex, "orders", "ix_ref", col("orders", "ref"))}
		a, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		Expect(changeIDs(a.Changes)).NotTo(ContainElement(indexRebuild))
		Expect(a.Changes).To(HaveLen(2))
	})

	It("does not rebuild for a forcing change that is not selected", func() {
		other := change("add", "column", "orders", "note")
		f.changes = append(f.changes, other)
		f.offline = []engine.Dependent{dependent(engine.DependentIndex, "orders", "ix_ref", col("orders", "ref"))}
		a, err := engine.Plan(f.request(engine.ScopeComplete, other.ID))
		Expect(err).NotTo(HaveOccurred())
		Expect(changeIDs(a.Changes)).To(Equal([]string{other.ID}))
	})

	It("ignores a dependent on columns no selected change forces", func() {
		f.offline = []engine.Dependent{dependent(engine.DependentIndex, "orders", "ix_other", col("orders", "other"))}
		a, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		Expect(a.Changes).To(HaveLen(1))
	})

	It("fails naming the dependent, the reason and the cause when it cannot be reproduced", func() {
		dep := dependent(engine.DependentIndex, "orders", "ix_ref", col("orders", "ref"))
		dep.Refusal = "page compression needs the DataCompression capability"
		f.offline = []engine.Dependent{dep}
		_, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).To(MatchError(And(
			ContainSubstring("dbo.orders.ix_ref"), ContainSubstring(dep.Refusal), ContainSubstring(refChange.ID))))
	})

	It("matches a dependent to a forcing column that is dropped only by equality, Dropped included", func() {
		dropped := col("orders", "ref")
		dropped.Dropped = engine.DroppedColumn
		f.forcing = []engine.Forcing{{Change: refChange.ID, Columns: []engine.ColumnRef{dropped}}}

		onDropped := dependent(engine.DependentIndex, "orders", "ix_ref", dropped)
		onDropped.Refusal = "the column is gone"
		f.offline = []engine.Dependent{onDropped}
		_, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).To(MatchError(ContainSubstring("the column is gone")))

		onPlain := dependent(engine.DependentIndex, "orders", "ix_ref", col("orders", "ref"))
		onPlain.Refusal = "the column is gone"
		f.offline = []engine.Dependent{onPlain}
		a, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		Expect(a.Changes).To(HaveLen(1))
	})

	It("acknowledges a rebuild through its destructive cause, never on its own", func() {
		destructive := refChange
		destructive.Destructive = true
		f.changes = []migrate.Change{destructive}
		f.offline = []engine.Dependent{dependent(engine.DependentIndex, "orders", "ix_ref", col("orders", "ref"))}
		a, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		Expect(engine.Acknowledgements(a)).To(Equal([]string{destructive.ID}))
	})

	It("fails when the diff forces on a change it does not list", func() {
		f.forcing = []engine.Forcing{{Change: "ghost", Columns: []engine.ColumnRef{col("orders", "ref")}}}
		_, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).To(MatchError(ContainSubstring("forcing names change ghost")))
	})
})

var _ = Describe("Foreign key partner rule", func() {
	var f *fakeDialect
	childChange := change("modify", "column", "child", "parent_id")
	parentChange := change("modify", "column", "parent", "id")
	childForcing := engine.Forcing{Change: childChange.ID, Columns: []engine.ColumnRef{col("child", "parent_id")}, Retype: true}
	parentForcing := engine.Forcing{Change: parentChange.ID, Columns: []engine.ColumnRef{col("parent", "id")}, Retype: true}
	fkRebuild := changeID("rebuild", "foreign-key", "child", "fk_child_parent")

	setup := func(forcing ...engine.Forcing) {
		f = newFake()
		f.changes = []migrate.Change{childChange, parentChange}
		f.forcing = forcing
		f.offline = []engine.Dependent{foreignKey("child", "fk_child_parent", "parent_id", "parent", "id",
			col("child", "parent_id"), col("parent", "id"))}
	}

	It("accepts a foreign key when both columns are retyped in the same plan", func() {
		setup(childForcing, parentForcing)
		a, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		Expect(changeIDs(a.Changes)).To(ContainElement(fkRebuild))
	})

	DescribeTable("refuses a foreign key whose other side is not retyped in the plan",
		func(forcing engine.Forcing, partner string) {
			setup(forcing)
			_, err := engine.Plan(f.request(engine.ScopeComplete))
			Expect(err).To(MatchError(And(ContainSubstring("dbo.child.fk_child_parent"), ContainSubstring(partner), ContainSubstring("same plan"))))
		},
		Entry("outbound: the child column changes alone", childForcing, "dbo.parent.id"),
		Entry("inbound: the referenced column changes alone", parentForcing, "dbo.child.parent_id"),
	)

	It("applies no partner rule when the forcing change does not retype", func() {
		forcing := childForcing
		forcing.Retype = false
		setup(forcing)
		a, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		Expect(changeIDs(a.Changes)).To(ContainElement(fkRebuild))
	})

	It("requires the partner to be retyped by a selected forcing, not merely forced", func() {
		weak := parentForcing
		weak.Retype = false
		setup(childForcing, weak)
		_, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).To(MatchError(ContainSubstring("dbo.parent.id")))
	})
})
