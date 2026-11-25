package engine_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// unusedQuerier proves the fake dialect, not the planner, decides what is read.
type unusedQuerier struct{}

func (unusedQuerier) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("unexpected query")
}

func (unusedQuerier) QueryRowContext(context.Context, string, ...any) *sql.Row { return &sql.Row{} }

func (unusedQuerier) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, errors.New("unexpected exec")
}

var _ = Describe("PlanLive", func() {
	var f *fakeDialect
	var live engine.PlanLiveRequest
	refChange := change("modify", "column", "orders", "ref")

	BeforeEach(func() {
		f = newFake()
		f.changes = []migrate.Change{refChange}
		f.forcing = []engine.Forcing{{Change: refChange.ID, Columns: []engine.ColumnRef{col("orders", "ref")}}}
		f.live = []engine.Dependent{dependent(engine.DependentIndex, "orders", "ix_ref", col("orders", "ref"))}
		f.inspect = func(context.Context, engine.Querier, engine.Scope) (engine.Inspection, error) {
			return engine.Inspection{Schema: migrate.Schema{Tables: []migrate.Table{{Name: tableName("orders")}}}}, nil
		}
		live = engine.PlanLiveRequest{Dialect: f.name, Scope: engine.Scope{
			Mode: engine.ScopeDeclared, Schemas: []string{"dbo"}, Objects: []migrate.Name{tableName("orders")},
		}}
	})

	It("plans from the inspected schema and the live dependents of the forced columns", func() {
		a, err := engine.PlanLive(context.Background(), unusedQuerier{}, live)
		Expect(err).NotTo(HaveOccurred())
		Expect(a.Baseline.Tables).To(HaveLen(1))
		Expect(a.Capabilities).To(Equal(f.caps))
		Expect(changeIDs(a.Changes)).To(ConsistOf(refChange.ID, changeID("rebuild", "index", "orders", "ix_ref")))
		Expect(f.calls[0]).To(Equal(dependentCall{offline: false, columns: []engine.ColumnRef{col("orders", "ref")}}))
		Expect(engine.Validate(a)).To(Succeed())
	})

	It("does not read live dependents when nothing is forced", func() {
		f.forcing = nil
		_, err := engine.PlanLive(context.Background(), unusedQuerier{}, live)
		Expect(err).NotTo(HaveOccurred())
		for _, call := range f.calls {
			Expect(call.offline).To(BeTrue())
		}
	})

	DescribeTable("fails on an inspection refusal only when a planned change or rebuild touches it",
		func(refusal engine.Refusal, touched bool) {
			f.inspect = func(context.Context, engine.Querier, engine.Scope) (engine.Inspection, error) {
				return engine.Inspection{Refusals: []engine.Refusal{refusal}}, nil
			}
			_, err := engine.PlanLive(context.Background(), unusedQuerier{}, live)
			if !touched {
				Expect(err).NotTo(HaveOccurred())
				return
			}
			Expect(err).To(MatchError(And(ContainSubstring("inspection refused"), ContainSubstring(refusal.Reason))))
		},
		Entry("the changed table", engine.Refusal{Object: tableName("orders"), Reason: "compressed"}, true),
		Entry("the changed member", engine.Refusal{Object: tableName("orders"), Member: "ref", Reason: "computed"}, true),
		Entry("the rebuilt index", engine.Refusal{Object: tableName("orders"), Member: "ix_ref", Reason: "filegroup"}, true),
		Entry("another member of the table", engine.Refusal{Object: tableName("orders"), Member: "note", Reason: "computed"}, false),
		Entry("an unrelated table", engine.Refusal{Object: tableName("audit"), Reason: "untrusted key"}, false),
	)

	Describe("dependents outside the scope", func() {
		auditChild := migrate.Name{Schema: "audit", Name: "child"}
		var inspected []engine.Scope

		BeforeEach(func() {
			inspected = nil
			ordersTable := func(name migrate.Name) migrate.Table {
				return migrate.Table{Name: name, Columns: []migrate.Column{{Name: "type", Type: "int"}}}
			}
			modify := change("modify", "column", "orders", "type")
			f.changes = []migrate.Change{modify}
			f.diffFn = func(current, desired migrate.Schema) []migrate.Change {
				changes := []migrate.Change{modify}
				for _, t := range current.Tables {
					if !slices.ContainsFunc(desired.Tables, func(d migrate.Table) bool { return d.Name == t.Name }) {
						changes = append(changes, migrate.Change{
							ID: changeID("drop", "table", t.Name.Schema+"."+t.Name.Name, ""), Action: "drop", Kind: "table", Object: t.Name, Destructive: true,
						})
					}
				}
				return changes
			}
			f.forcing = []engine.Forcing{{Change: modify.ID, Columns: []engine.ColumnRef{col("orders", "type")}}}
			fk := foreignKey("child", "fk_child_orders", "order_id", "orders", "id", col("orders", "type"))
			fk.Table = auditChild
			f.live = []engine.Dependent{fk}
			f.inspect = func(_ context.Context, _ engine.Querier, scope engine.Scope) (engine.Inspection, error) {
				inspected = append(inspected, scope)
				all := []migrate.Table{ordersTable(tableName("orders")), ordersTable(auditChild)}
				var out []migrate.Table
				for _, t := range all {
					inScope := scope.Mode == engine.ScopeComplete && slices.Contains(scope.Schemas, t.Name.Schema) ||
						scope.Mode == engine.ScopeDeclared && slices.Contains(scope.Objects, t.Name)
					if inScope {
						out = append(out, t)
					}
				}
				return engine.Inspection{Schema: migrate.Schema{Tables: out}}, nil
			}
		})

		DescribeTable("inspects them again as context and never plans or preserves their drops",
			func(scope engine.Scope) {
				live.Scope, live.Desired = scope, worldSchema(map[string]string{"orders": "bigint"})
				a, err := engine.PlanLive(context.Background(), unusedQuerier{}, live)
				Expect(err).NotTo(HaveOccurred())
				Expect(inspected).To(HaveLen(2))
				Expect(inspected[1]).To(Equal(engine.Scope{Mode: engine.ScopeDeclared, Schemas: scope.Schemas, Objects: []migrate.Name{auditChild}}))
				Expect(a.Scope).To(Equal(scope))
				Expect(a.Context).To(Equal([]migrate.Name{auditChild}))
				Expect(a.Baseline.Tables).To(HaveLen(2))
				rebuild, err := json.Marshal([]string{"rebuild", "foreign-key", "audit", "child", "fk_child_orders"})
				Expect(err).NotTo(HaveOccurred())
				Expect(changeIDs(a.Changes)).To(ConsistOf(changeID("modify", "column", "orders", "type"), string(rebuild)))
				Expect(a.Preserved).To(BeEmpty())
				Expect(f.reconciled).To(HaveLen(2), "again after the context re-inspection")
				Expect(f.reconciled[1][0].Tables).To(HaveLen(2))
				Expect(engine.Validate(a)).To(Succeed())
			},
			Entry("complete scope", engine.Scope{Mode: engine.ScopeComplete, Schemas: []string{"dbo"}}),
			Entry("declared scope", engine.Scope{Mode: engine.ScopeDeclared, Schemas: []string{"dbo"}, Objects: []migrate.Name{tableName("orders")}}),
		)
	})

	It("passes the requested scope to the inspector", func() {
		_, err := engine.PlanLive(context.Background(), unusedQuerier{}, live)
		Expect(err).NotTo(HaveOccurred())
		Expect(f.scopes[0]).To(Equal(live.Scope))
	})
})
