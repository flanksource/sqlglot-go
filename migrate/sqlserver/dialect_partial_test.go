package sqlserver_test

import (
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL Server declarations that are complete or partial", func() {
	// live is orders as a database holds it: the declared id, and a column, an
	// index and a check the declaration does not mention.
	live := func() migrate.Schema {
		t := charTable(charOrders, charColumn("id", "int", false), charColumn("legacy", "int", true))
		t.Indexes = []migrate.Index{{Name: "IX_orders_legacy", Columns: []migrate.IndexColumn{{Name: "legacy"}}}}
		t.Checks = []migrate.Check{{Name: "CK_orders_legacy", Expression: "([legacy]>(0))"}}
		return schemaOf(t)
	}
	declared := func(partial bool, columns ...migrate.Column) migrate.Schema {
		t := charTable(charOrders, columns...)
		t.Partial = partial
		return schemaOf(t)
	}
	declaredScope := func(current, desired migrate.Schema) engine.PlanRequest {
		request := completeRequest(current, desired)
		request.Scope = engine.Scope{Mode: engine.ScopeDeclared, Schemas: []string{"dbo"}, Objects: []migrate.Name{charOrders}}
		return request
	}
	legacyDrops := []string{
		changeID("drop", "column", "orders", "legacy"),
		changeID("drop", "index", "orders", "IX_orders_legacy"),
		changeID("drop", "check", "orders", "CK_orders_legacy"),
	}

	It("drops the undeclared column, index and check of a complete declaration, each acknowledged, the index first", func() {
		artifact := planArtifact(declaredScope(live(), declared(false, charColumn("id", "int", false))))
		Expect(artifact.Preserved).To(BeEmpty())
		Expect(sortedChangeIDs(artifact)).To(ConsistOf(legacyDrops))
		Expect(statementsOf(artifact)).To(ConsistOf(
			"DROP INDEX [IX_orders_legacy] ON [dbo].[orders]",
			"ALTER TABLE [dbo].[orders] DROP CONSTRAINT [CK_orders_legacy]",
			"ALTER TABLE [dbo].[orders] DROP COLUMN [legacy]"))
		Expect(engine.Acknowledgements(artifact)).To(ConsistOf(legacyDrops))
		Expect(engine.Validate(artifact)).To(Succeed())
	})

	It("plans nothing for a partial declaration and lists the three drops as preserved", func() {
		artifact := planArtifact(declaredScope(live(), declared(true, charColumn("id", "int", false))))
		Expect(artifact.Changes).To(BeEmpty())
		Expect(artifact.Units).To(BeEmpty())
		ids := []string{}
		for _, c := range artifact.Preserved {
			ids = append(ids, c.ID)
		}
		Expect(ids).To(ConsistOf(legacyDrops))
		Expect(engine.Validate(artifact)).To(Succeed())
	})

	It("still rebuilds the undeclared index that covers a column a partial declaration retypes", func() {
		desired := declared(true, charColumn("id", "int", false), charColumn("legacy", "bigint", true))
		artifact := planArtifact(declaredScope(live(), desired))
		Expect(sortedChangeIDs(artifact)).To(ConsistOf(
			changeID("modify", "column", "orders", "legacy"),
			changeID("rebuild", "index", "orders", "IX_orders_legacy"),
			changeID("rebuild", "check", "orders", "CK_orders_legacy")))
		Expect(statementsOf(artifact)).To(ContainElements(
			"DROP INDEX [IX_orders_legacy] ON [dbo].[orders]",
			"ALTER TABLE [dbo].[orders] DROP CONSTRAINT [CK_orders_legacy]",
			"ALTER TABLE [dbo].[orders] ALTER COLUMN [legacy] bigint NULL",
			ContainSubstring("CREATE NONCLUSTERED INDEX [IX_orders_legacy]"),
			ContainSubstring("ADD CONSTRAINT [CK_orders_legacy] CHECK")))
		preserved := []string{}
		for _, c := range artifact.Preserved {
			preserved = append(preserved, c.ID)
		}
		Expect(preserved).To(ConsistOf(legacyDrops[1], legacyDrops[2]), "the rebuild re-creates what the declaration leaves alone")
	})

	Describe("a column another table's foreign key references", func() {
		fk := migrate.ForeignKey{Name: "FK_orders_customers", Columns: []string{"legacy"}, References: charCustomers, ReferencedColumns: []string{"legacy"}}
		customersLive := charTable(charCustomers, charColumn("id", "int", false), charColumn("legacy", "int", false))
		customersDeclared := charTable(charCustomers, charColumn("id", "int", false))
		inbound := engine.Dependent{
			Kind: engine.DependentForeignKey, Table: charOrders, Name: fk.Name, ForeignKey: &fk,
			On: []engine.ColumnRef{{Table: charCustomers, Column: "legacy", Dropped: engine.DroppedColumn}},
		}
		ordersLive := func() migrate.Table {
			t := charTable(charOrders, charColumn("id", "int", false), charColumn("legacy", "int", false))
			t.ForeignKeys = []migrate.ForeignKey{fk}
			return t
		}
		unreferenced := func(partial bool) migrate.Table {
			t := charTable(charOrders, charColumn("id", "int", false), charColumn("legacy", "int", false))
			t.Partial = partial
			return t
		}
		refusal := And(ContainSubstring("foreign-key dbo.orders.FK_orders_customers"), ContainSubstring("dbo.customers.legacy"), ContainSubstring("cannot be re-created"))

		It("refuses the drop when the table owning the key is outside the plan, naming it", func() {
			request := completeRequest(schemaOf(customersLive), schemaOf(customersDeclared))
			request.Scope = engine.Scope{Mode: engine.ScopeDeclared, Schemas: []string{"dbo"}, Objects: []migrate.Name{charCustomers}}
			request.Dependents = []engine.Dependent{inbound}
			_, err := engine.Plan(request)
			Expect(err).To(MatchError(refusal))
		})

		It("refuses the drop when the table owning the key is partial, so the key it keeps is never dropped, naming the key", func() {
			request := completeRequest(schemaOf(customersLive, ordersLive()), schemaOf(customersDeclared, unreferenced(true)))
			request.Scope = engine.Scope{Mode: engine.ScopeDeclared, Schemas: []string{"dbo"}, Objects: []migrate.Name{charCustomers, charOrders}}
			_, err := engine.Plan(request)
			Expect(err).To(MatchError(And(
				ContainSubstring(changeID("drop", "foreign-key", "orders", "FK_orders_customers")),
				ContainSubstring("dbo.orders, which is declared partial"))))
		})

		It("drops the key and the column together when the table owning the key declares it away", func() {
			request := completeRequest(schemaOf(customersLive, ordersLive()), schemaOf(customersDeclared, unreferenced(false)))
			request.Scope = engine.Scope{Mode: engine.ScopeDeclared, Schemas: []string{"dbo"}, Objects: []migrate.Name{charCustomers, charOrders}}
			artifact := planArtifact(request)
			Expect(sortedChangeIDs(artifact)).To(ConsistOf(
				changeID("drop", "foreign-key", "orders", "FK_orders_customers"), changeID("drop", "column", "customers", "legacy")))
			Expect(engine.Validate(artifact)).To(Succeed())
		})
	})
})
