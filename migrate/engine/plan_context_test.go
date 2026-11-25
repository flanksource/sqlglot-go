package engine_test

import (
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Context objects", func() {
	var f *fakeDialect
	add := change("add", "column", "orders", "note")
	dropChild := change("drop", "table", "child", "")

	BeforeEach(func() {
		f = newFake()
		f.changes = []migrate.Change{add, dropChild}
	})

	request := func(mode engine.ScopeMode, selection ...string) engine.PlanRequest {
		req := f.request(mode, selection...)
		req.Context = []migrate.Name{tableName("child"), tableName("audit")}
		return req
	}

	DescribeTable("leave the changes on a context object out of both planned and preserved",
		func(mode engine.ScopeMode) {
			a, err := engine.Plan(request(mode))
			Expect(err).NotTo(HaveOccurred())
			Expect(changeIDs(a.Changes)).To(Equal([]string{add.ID}))
			Expect(a.Preserved).To(BeEmpty())
			Expect(a.Context).To(Equal([]migrate.Name{tableName("audit"), tableName("child")}))
			Expect(engine.Validate(a)).To(Succeed())
		},
		Entry("complete scope", engine.ScopeComplete),
		Entry("declared scope", engine.ScopeDeclared),
	)

	It("refuses to select a change on a context object", func() {
		_, err := engine.Plan(request(engine.ScopeComplete, dropChild.ID))
		Expect(err).To(MatchError(ContainSubstring("context object dbo.child")))
	})

	It("fails a planned change that requires a change on a context object", func() {
		f.changes = []migrate.Change{change("modify", "column", "orders", "id", dropChild.ID), dropChild}
		_, err := engine.Plan(request(engine.ScopeComplete))
		Expect(err).To(MatchError(And(ContainSubstring("requires"), ContainSubstring("context object"))))
	})

	It("hashes the context", func() {
		f.changes = []migrate.Change{add}
		with, err := engine.Plan(request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		without, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		Expect(with.Hash).NotTo(Equal(without.Hash))
	})

	It("still plans a rebuild on a context object", func() {
		f.changes = []migrate.Change{change("modify", "column", "orders", "ref")}
		f.forcing = []engine.Forcing{{Change: f.changes[0].ID, Columns: []engine.ColumnRef{col("orders", "ref")}}}
		f.offline = []engine.Dependent{foreignKey("child", "fk_child_orders", "order_id", "orders", "ref", col("orders", "ref"))}
		a, err := engine.Plan(request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		Expect(changeIDs(a.Changes)).To(ContainElement(changeID("rebuild", "foreign-key", "child", "fk_child_orders")))
	})
})

var _ = Describe("Refusals and warnings in the artifact", func() {
	var f *fakeDialect
	change1 := change("modify", "column", "orders", "ref")

	BeforeEach(func() {
		f = newFake()
		f.changes = []migrate.Change{change1}
	})

	It("fails when a planned change touches a refused object or member", func() {
		req := f.request(engine.ScopeComplete)
		req.Refusals = []engine.Refusal{{Object: tableName("orders"), Member: "ref", Reason: "computed column"}}
		_, err := engine.Plan(req)
		Expect(err).To(MatchError(And(ContainSubstring("inspection refused"), ContainSubstring("dbo.orders.ref"), ContainSubstring("computed column"))))
	})

	It("records untouched refusals sorted and hashes them, and re-plans from them", func() {
		req := f.request(engine.ScopeComplete)
		req.Refusals = []engine.Refusal{
			{Object: tableName("zeta"), Reason: "untrusted key"},
			{Object: tableName("audit"), Member: "ix", Reason: "filegroup"},
		}
		a, err := engine.Plan(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(a.Refusals).To(Equal([]engine.Refusal{req.Refusals[1], req.Refusals[0]}))
		Expect(engine.Validate(a)).To(Succeed())

		req.Refusals = req.Refusals[:1]
		fewer, err := engine.Plan(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(fewer.Hash).NotTo(Equal(a.Hash))
	})

	It("records the diff's warnings sorted and de-duplicated", func() {
		f.warnings = []string{"view v2 compared as written", "view v1 compared as written", "view v2 compared as written"}
		a, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		Expect(a.Warnings).To(Equal([]string{"view v1 compared as written", "view v2 compared as written"}))
		Expect(engine.Validate(a)).To(Succeed())
	})
})
