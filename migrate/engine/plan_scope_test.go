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

	It("preserves every kind of drop in declared scope and plans only the rest", func() {
		setup()
		a, err := engine.Plan(f.request(engine.ScopeDeclared))
		Expect(err).NotTo(HaveOccurred())
		Expect(changeIDs(a.Changes)).To(Equal([]string{changeID("add", "column", "orders", "note")}))
		Expect(changeIDs(a.Preserved)).To(ConsistOf(dropped()))
		Expect(engine.Acknowledgements(a)).To(BeEmpty())
	})

	It("plans every drop in complete scope and requires each to be acknowledged", func() {
		setup()
		a, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		Expect(a.Preserved).To(BeEmpty())
		Expect(engine.Acknowledgements(a)).To(ConsistOf(dropped()))
	})

	It("rejects an unknown scope mode", func() {
		_, err := engine.Plan(f.request("everything"))
		Expect(err).To(MatchError(ContainSubstring(`scope mode must be "declared" or "complete"`)))
	})

	It("refuses to select a drop the declared scope preserves", func() {
		setup()
		_, err := engine.Plan(f.request(engine.ScopeDeclared, changeID("drop", "index", "orders", "ix_legacy")))
		Expect(err).To(MatchError(ContainSubstring("declared scope preserves")))
	})

	It("fails when a planned change requires a drop the declared scope preserves", func() {
		f.changes = []migrate.Change{
			change("modify", "column", "orders", "id", changeID("drop", "index", "orders", "ix_id")),
			change("drop", "index", "orders", "ix_id"),
		}
		_, err := engine.Plan(f.request(engine.ScopeDeclared))
		Expect(err).To(MatchError(And(ContainSubstring("requires"), ContainSubstring("declared scope preserves"))))
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

	It("plans only the selected changes", func() {
		setup()
		id := changeID("add", "column", "orders", "note")
		a, err := engine.Plan(f.request(engine.ScopeComplete, id))
		Expect(err).NotTo(HaveOccurred())
		Expect(changeIDs(a.Changes)).To(Equal([]string{id}))
		Expect(a.Selection).To(Equal([]string{id}))
	})
})
