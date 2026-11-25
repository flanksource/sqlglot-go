package engine_test

import (
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Units and preconditions", func() {
	var f *fakeDialect
	ordersAdd := change("add", "column", "orders", "note")
	itemsAdd := change("add", "column", "items", "note")

	BeforeEach(func() {
		f = newFake()
		f.changes = []migrate.Change{ordersAdd, itemsAdd}
	})

	It("splits unrelated tables into separate units, ordered by first op, with deterministic IDs", func() {
		a, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		Expect(a.Units).To(HaveLen(2))
		Expect(a.Units[0].Changes).To(Equal([]string{ordersAdd.ID}))
		Expect(a.Units[0].Objects).To(Equal([]migrate.Name{tableName("orders")}))
		Expect(a.Units[1].Changes).To(Equal([]string{itemsAdd.ID}))
		Expect(a.Units[0].ID).To(HaveLen(12))
		Expect(a.Units[0].ID).NotTo(Equal(a.Units[1].ID))

		again, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		Expect(again.Units[0].ID).To(Equal(a.Units[0].ID))
	})

	It("puts everything in one unit when Atomic", func() {
		req := f.request(engine.ScopeComplete)
		req.Atomic = true
		a, err := engine.Plan(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(a.Units).To(HaveLen(1))
		Expect(a.Units[0].Changes).To(ConsistOf(ordersAdd.ID, itemsAdd.ID))
	})

	It("links ops that require each other even across tables", func() {
		f.lower = func([]string, []engine.Rebuild) ([]engine.Op, error) {
			return []engine.Op{
				{ID: "a", Change: ordersAdd.ID, Phase: engine.PhaseCreate, Object: ordersAdd.Object},
				{ID: "b", Change: itemsAdd.ID, Phase: engine.PhaseCreate, Object: itemsAdd.Object, Requires: []string{"a"}},
			}, nil
		}
		a, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		Expect(a.Units).To(HaveLen(1))
	})

	It("links a rebuild to the unit of the change that caused it", func() {
		f.forcing = []engine.Forcing{{Change: ordersAdd.ID, Columns: []engine.ColumnRef{col("orders", "id")}}}
		f.offline = []engine.Dependent{foreignKey("items", "fk_items_orders", "order_id", "orders", "id", col("orders", "id"))}
		a, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		Expect(a.Units).To(HaveLen(1))
		Expect(a.Units[0].Objects).To(Equal([]migrate.Name{tableName("items"), tableName("orders")}))
	})

	It("marks a unit non-transactional when a statement is, and leaves the others transactional", func() {
		f.lower = func([]string, []engine.Rebuild) ([]engine.Op, error) {
			return []engine.Op{
				{ID: "a", Change: ordersAdd.ID, Phase: engine.PhaseCreate, Object: ordersAdd.Object, Statements: []engine.Statement{{SQL: "CREATE INDEX ... ONLINE", NonTransactional: true}}},
				{ID: "b", Change: itemsAdd.ID, Phase: engine.PhaseCreate, Object: itemsAdd.Object, Statements: []engine.Statement{{SQL: "ALTER"}}},
			}, nil
		}
		a, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		Expect([]bool{a.Units[0].Transactional, a.Units[1].Transactional}).To(Equal([]bool{false, true}))
	})

	It("is never transactional when the dialect has no transactional DDL", func() {
		f.caps.TransactionalDDL = false
		a, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		Expect(a.Units[0].Transactional || a.Units[1].Transactional).To(BeFalse())
	})

	It("attaches each change's preconditions to its unit, sorted by ID", func() {
		f.pre = func(c migrate.Change) []engine.Precondition {
			if c.ID != itemsAdd.ID {
				return nil
			}
			return []engine.Precondition{
				{ID: "z-nulls", Change: c.ID, SQL: "SELECT 1 WHERE 0=1", Message: "nulls"},
				{ID: "a-guid", Change: c.ID, SQL: "SELECT 1 WHERE 0=1", Message: "not a guid"},
			}
		}
		a, err := engine.Plan(f.request(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		Expect(a.Units[0].Preconditions).To(BeEmpty())
		Expect([]string{a.Units[1].Preconditions[0].ID, a.Units[1].Preconditions[1].ID}).To(Equal([]string{"a-guid", "z-nulls"}))
	})

	DescribeTable("rejects preconditions that cannot be addressed",
		func(pre func(c migrate.Change) []engine.Precondition, want string) {
			f.pre = pre
			_, err := engine.Plan(f.request(engine.ScopeComplete))
			Expect(err).To(MatchError(ContainSubstring(want)))
		},
		Entry("naming another change", func(c migrate.Change) []engine.Precondition {
			return []engine.Precondition{{ID: "p", Change: "other", SQL: "SELECT 1"}}
		}, "names change"),
		Entry("without SQL", func(c migrate.Change) []engine.Precondition {
			return []engine.Precondition{{ID: "p", Change: c.ID}}
		}, "needs an ID and SQL"),
		Entry("with a repeated ID", func(c migrate.Change) []engine.Precondition {
			return []engine.Precondition{{ID: "p", Change: c.ID, SQL: "SELECT 1"}}
		}, "duplicate precondition ID p"),
	)
})
