package engine_test

import (
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Plan scope and selection", func() {
	var f *fakeDialect

	BeforeEach(func() { f = newFake() })

	dropped := func() []string {
		return []string{
			changeID("drop", "column", "orders", "legacy"),
			changeID("drop", "index", "orders", "ix_legacy"),
			changeID("drop", "foreign-key", "orders", "fk_legacy"),
			changeID("drop", "check", "orders", "ck_legacy"),
			changeID("drop", "table", "retired", ""),
		}
	}

	setup := func() {
		f.changes = append(f.changes,
			change("add", "column", "orders", "note"),
			change("drop", "column", "orders", "legacy"),
			change("drop", "index", "orders", "ix_legacy"),
			change("drop", "foreign-key", "orders", "fk_legacy"),
			change("drop", "check", "orders", "ck_legacy"),
			change("drop", "table", "retired", ""),
		)
	}

	// declare plans toward a complete declaration of the orders table, which the
	// fake's scope manages and the database has, with the other tables of the
	// diff left undeclared.
	declare := func(req engine.PlanRequest) engine.PlanRequest {
		req.Current = worldSchema(map[string]string{"orders": "int"})
		req.Desired = worldSchema(map[string]string{"orders": "int"})
		return req
	}

	It("plans the drop of every member a complete declaration omits in declared scope, and preserves the drop of an undeclared table", func() {
		setup()
		a, err := engine.Plan(declare(f.request(engine.ScopeDeclared)))
		Expect(err).NotTo(HaveOccurred())
		Expect(changeIDs(a.Changes)).To(ConsistOf(
			changeID("add", "column", "orders", "note"),
			changeID("drop", "column", "orders", "legacy"),
			changeID("drop", "index", "orders", "ix_legacy"),
			changeID("drop", "foreign-key", "orders", "fk_legacy"),
			changeID("drop", "check", "orders", "ck_legacy")))
		Expect(changeIDs(a.Preserved)).To(Equal([]string{changeID("drop", "table", "retired", "")}))
		Expect(engine.Acknowledgements(a)).To(ConsistOf(dropped()[:4]))
	})

	It("plans every drop in complete scope and requires each to be acknowledged", func() {
		setup()
		a, err := engine.Plan(declare(f.request(engine.ScopeComplete)))
		Expect(err).NotTo(HaveOccurred())
		Expect(a.Preserved).To(BeEmpty())
		Expect(engine.Acknowledgements(a)).To(ConsistOf(dropped()))
	})

	DescribeTable("preserves the drop of every member of a partial table, in either scope, plans the rest, and replans byte for byte",
		func(mode engine.ScopeMode, preserved []string) {
			setup()
			req := declare(f.request(mode))
			req.Desired.Tables[0].Partial = true
			a, err := engine.Plan(req)
			Expect(err).NotTo(HaveOccurred())
			Expect(changeIDs(a.Preserved)).To(ConsistOf(preserved))
			Expect(changeIDs(a.Changes)).To(ContainElement(changeID("add", "column", "orders", "note")))
			Expect(engine.Acknowledgements(a)).NotTo(ContainElement(ContainSubstring(`"orders"`)))
			Expect(a.Desired.Tables[0].Partial).To(BeTrue())
			Expect(engine.Validate(a)).To(Succeed())
		},
		// the declared scope also keeps the table nothing declares
		Entry("declared", engine.ScopeDeclared, dropped()),
		Entry("complete", engine.ScopeComplete, dropped()[:4]),
	)

	It("fails a plan that declares a partial table the target does not have", func() {
		req := declare(f.request(engine.ScopeDeclared))
		req.Current = migrate.Schema{}
		req.Desired.Tables[0].Partial = true
		_, err := engine.Plan(req)
		Expect(err).To(MatchError("engine: table dbo.orders is declared partial and does not exist: a partial declaration cannot create a table"))
	})

	It("refuses to select the drop of a member of a partial table", func() {
		setup()
		req := declare(f.request(engine.ScopeComplete, changeID("drop", "index", "orders", "ix_legacy")))
		req.Desired.Tables[0].Partial = true
		_, err := engine.Plan(req)
		Expect(err).To(MatchError(ContainSubstring("a drop of a member of table dbo.orders, which is declared partial")))
	})

	It("rejects an unknown scope mode", func() {
		_, err := engine.Plan(f.request("everything"))
		Expect(err).To(MatchError(ContainSubstring(`scope mode must be "declared" or "complete"`)))
	})

	It("refuses to select the drop of a table the declared scope preserves", func() {
		setup()
		_, err := engine.Plan(declare(f.request(engine.ScopeDeclared, changeID("drop", "table", "retired", ""))))
		Expect(err).To(MatchError(ContainSubstring("declared scope preserves")))
	})

	It("fails when a planned change requires a drop the declared scope preserves", func() {
		f.changes = []migrate.Change{
			change("modify", "table", "orders", "", changeID("drop", "table", "retired", "")),
			change("drop", "table", "retired", ""),
		}
		_, err := engine.Plan(declare(f.request(engine.ScopeDeclared)))
		Expect(err).To(MatchError(And(ContainSubstring("requires"), ContainSubstring("declared scope preserves"))))
	})

	It("fails when a planned change requires a drop a partial table preserves", func() {
		f.changes = []migrate.Change{
			change("modify", "column", "orders", "id", changeID("drop", "index", "orders", "ix_id")),
			change("drop", "index", "orders", "ix_id"),
		}
		req := declare(f.request(engine.ScopeComplete))
		req.Desired.Tables[0].Partial = true
		_, err := engine.Plan(req)
		Expect(err).To(MatchError(And(ContainSubstring("requires"), ContainSubstring("which is declared partial"))))
	})

	It("plans every change when the selection is empty", func() {
		setup()
		a, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		Expect(a.Changes).To(HaveLen(6))
		Expect(a.Selection).To(BeEmpty())
	})

	It("fails closed on an unknown selected ID", func() {
		setup()
		_, err := engine.Plan(f.request(engine.ScopeComplete, "nope"))
		Expect(err).To(MatchError(ContainSubstring("not in the diff: nope")))
	})

	It("names the missing IDs when a selected change requires an unselected one", func() {
		addColumn := change("add", "column", "orders", "note")
		addIndex := change("add", "index", "orders", "ix_note", addColumn.ID)
		f.changes = []migrate.Change{addColumn, addIndex}
		_, err := engine.Plan(f.request(engine.ScopeComplete, addIndex.ID))
		Expect(err).To(MatchError(And(ContainSubstring(addIndex.ID), ContainSubstring("requires unselected changes"), ContainSubstring(addColumn.ID))))

		a, err := engine.Plan(f.request(engine.ScopeComplete, addIndex.ID, addColumn.ID))
		Expect(err).NotTo(HaveOccurred())
		Expect(changeIDs(a.Changes)).To(ConsistOf(addColumn.ID, addIndex.ID))
	})

	It("plans no change at all when told to select none, and replans that artifact", func() {
		setup()
		req := declare(f.request(engine.ScopeComplete))
		req.SelectNone = true
		a, err := engine.Plan(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(a.Changes).To(BeEmpty())
		Expect(a.Units).To(BeEmpty())
		Expect(a.SelectNone).To(BeTrue())
		Expect(engine.Validate(a)).To(Succeed())

		req.Selection = []string{changeID("add", "column", "orders", "note")}
		_, err = engine.Plan(req)
		Expect(err).To(MatchError(ContainSubstring("exclude each other")))
	})

	It("plans only the selected changes", func() {
		setup()
		id := changeID("add", "column", "orders", "note")
		a, err := engine.Plan(f.request(engine.ScopeComplete, id))
		Expect(err).NotTo(HaveOccurred())
		Expect(changeIDs(a.Changes)).To(Equal([]string{id}))
		Expect(a.Selection).To(Equal([]string{id}))
	})
})
