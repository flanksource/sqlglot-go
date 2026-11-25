package engine_test

import (
	"context"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Declared scope", func() {
	var f *fakeDialect

	BeforeEach(func() { f = newFake() })

	It("narrows Desired to the scope's objects before diffing, keeping Native whole", func() {
		req := f.request(engine.ScopeDeclared)
		req.Desired = worldSchema(map[string]string{"orders": "int", "parent": "int"})
		req.Desired.Native = "hcl"
		a, err := engine.Plan(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(a.Desired.Tables).To(HaveLen(1))
		Expect(a.Desired.Tables[0].Name).To(Equal(tableName("orders")))
		Expect(a.Desired.Native).To(Equal("hcl"))
		Expect(f.diffCalls[0][1].Tables).To(HaveLen(1))
		Expect(engine.Validate(a)).To(Succeed())
	})

	It("is an error without objects to manage", func() {
		req := f.request(engine.ScopeDeclared)
		req.Scope.Objects = nil
		_, err := engine.Plan(req)
		Expect(err).To(MatchError(ContainSubstring("declared scope must name the objects it manages")))
	})

	It("is defaulted by PlanLive to every object of the desired schema, and recorded in the artifact scope", func() {
		f.inspect = func(context.Context, engine.Querier, engine.Scope) (engine.Inspection, error) {
			return engine.Inspection{}, nil
		}
		desired := worldSchema(map[string]string{"orders": "int", "parent": "int"})
		desired.Definitions = []migrate.Definition{{Name: tableName("v_orders")}}
		a, err := engine.PlanLive(context.Background(), unusedQuerier{}, engine.PlanLiveRequest{
			Dialect: f.name, Scope: engine.Scope{Mode: engine.ScopeDeclared, Schemas: []string{"dbo"}}, Desired: desired,
		})
		Expect(err).NotTo(HaveOccurred())
		managed := []migrate.Name{tableName("orders"), tableName("parent"), tableName("v_orders")}
		Expect(a.Scope.Objects).To(Equal(managed))
		Expect(f.scopes[0].Objects).To(Equal(managed))
		Expect(engine.Validate(a)).To(Succeed())
	})
})

var _ = Describe("PlanLive reconciliation", func() {
	It("plans with the reconciled desired schema, which the artifact holds and Validate accepts offline", func() {
		f := newFake()
		f.inspect = func(context.Context, engine.Querier, engine.Scope) (engine.Inspection, error) {
			return engine.Inspection{Schema: worldSchema(map[string]string{"orders": "int"})}, nil
		}
		f.reconcile = func(_, desired migrate.Schema) migrate.Schema {
			desired.Tables[0].Columns[0].Collation = "inherited-from-live"
			return desired
		}
		a, err := engine.PlanLive(context.Background(), unusedQuerier{}, engine.PlanLiveRequest{
			Dialect: f.name, Desired: worldSchema(map[string]string{"orders": "int"}),
			Scope: engine.Scope{Mode: engine.ScopeDeclared, Schemas: []string{"dbo"}, Objects: []migrate.Name{tableName("orders")}},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.reconciled).To(HaveLen(1))
		Expect(f.reconciled[0][0].Tables[0].Name).To(Equal(tableName("orders")))
		Expect(a.Desired.Tables[0].Columns[0].Collation).To(Equal("inherited-from-live"))
		Expect(engine.Validate(a)).To(Succeed())
	})
})
