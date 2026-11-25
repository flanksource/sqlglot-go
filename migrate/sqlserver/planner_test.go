package sqlserver_test

import (
	"github.com/flanksource/sqlglot-go/migrate"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Native migration contracts", func() {
	ordersName := migrate.Name{Schema: "dbo", Name: "orders"}

	It("plans a typed table through the engine", func() {
		artifact := mustPlanBetween(migrate.Schema{}, schemaOf(charTable(ordersName, charColumn("id", "int", false))))
		Expect(statementsOf(artifact)).To(Equal([]string{"CREATE TABLE [dbo].[orders] ([id] int NOT NULL)"}))
		Expect(artifact.Changes[0].Destructive).To(BeFalse())
	})

	It("marks dropping a target column as destructive", func() {
		current := charTable(ordersName, charColumn("id", "int", false), charColumn("note", "nvarchar(20)", true))
		desired := charTable(ordersName, charColumn("id", "int", false))
		artifact := mustPlanBetween(schemaOf(current), schemaOf(desired))
		Expect(artifact.Changes).To(HaveLen(1))
		Expect(artifact.Changes[0]).To(And(HaveField("Destructive", BeTrue()), HaveField("Member", "note")))
	})

	It("escapes closing brackets in catalog identifiers", func() {
		odd := charTable(migrate.Name{Schema: "dbo", Name: "odd]orders"}, charColumn("a]b", "int", false))
		Expect(statementsOf(mustPlanBetween(migrate.Schema{}, schemaOf(odd)))).To(Equal([]string{"CREATE TABLE [dbo].[odd]]orders] ([a]]b] int NOT NULL)"}))
	})

	It("preserves named defaults, collations and unique constraints", func() {
		table := charTable(ordersName, migrate.Column{Name: "code", Type: "nvarchar(20)", Collation: "Latin1_General_100_BIN2", Default: &migrate.Default{Name: "DF_order_code", Expression: "N'new'"}})
		table.Indexes = []migrate.Index{{Name: "UQ_order_code", Unique: true, Constraint: true, Columns: []migrate.IndexColumn{{Name: "code"}}}}
		statements := statementsOf(mustPlanBetween(migrate.Schema{}, schemaOf(table)))
		Expect(statements).To(HaveLen(2))
		Expect(statements[0]).To(ContainSubstring("[code] nvarchar(20) COLLATE Latin1_General_100_BIN2 NOT NULL CONSTRAINT [DF_order_code] DEFAULT N'new'"))
		Expect(statements[1]).To(Equal("ALTER TABLE [dbo].[orders] ADD CONSTRAINT [UQ_order_code] UNIQUE NONCLUSTERED ([code])"))
	})

	It("restores the database collation and preserves the named default", func() {
		current := charTable(ordersName, migrate.Column{Name: "code", Type: "nvarchar(20)", Collation: "Latin1_General_100_BIN2", Default: &migrate.Default{Name: "DF_orders_code", Expression: "N'new'"}})
		desired := charTable(ordersName, migrate.Column{Name: "code", Type: "nvarchar(20)", Default: &migrate.Default{Name: "DF_orders_code", Expression: "N'new'"}})
		artifact := mustPlanBetween(schemaOf(current), schemaOf(desired))
		Expect(statementsOf(artifact)).To(Equal([]string{
			"ALTER TABLE [dbo].[orders] DROP CONSTRAINT [DF_orders_code]",
			"ALTER TABLE [dbo].[orders] ALTER COLUMN [code] nvarchar(20) COLLATE DATABASE_DEFAULT NOT NULL",
			"ALTER TABLE [dbo].[orders] ADD CONSTRAINT [DF_orders_code] DEFAULT N'new' FOR [code]",
		}))
		Expect(artifact.Changes[0].Destructive).To(BeTrue())
	})

	It("plans full view and trigger definitions in their own batches", func() {
		table := charTable(ordersName, charColumn("id", "int", false))
		view := migrate.Definition{Name: migrate.Name{Schema: "dbo", Name: "order_ids"}, Kind: "VIEW", SQL: "CREATE VIEW dbo.order_ids AS SELECT id FROM dbo.orders"}
		trigger := migrate.Definition{Name: migrate.Name{Schema: "dbo", Name: "audit_orders"}, Kind: "TRIGGER", Parent: &table.Name, SQL: "CREATE TRIGGER dbo.audit_orders ON dbo.orders AFTER INSERT AS BEGIN SELECT 1; END"}
		desired := migrate.Schema{Tables: []migrate.Table{table}, Definitions: []migrate.Definition{view, trigger}}
		Expect(statementsOf(mustPlanBetween(schemaOf(table), desired))).To(ConsistOf(
			"ENABLE TRIGGER [dbo].[audit_orders] ON [dbo].[orders]",
			"CREATE OR ALTER VIEW dbo.order_ids AS SELECT id FROM dbo.orders",
			"CREATE OR ALTER TRIGGER dbo.audit_orders ON dbo.orders AFTER INSERT AS BEGIN SELECT 1; END",
		))
	})

	It("flags modules with runtime-dispatched dependencies without ordering on them", func() {
		runtime := []migrate.RuntimeDependency{{Schema: "dbo", Name: "missing_rates"}, {Name: "OPENJSON"}}
		proc := migrate.Definition{Name: migrate.Name{Schema: "dbo", Name: "load_rates"}, Kind: "PROCEDURE", SQL: "CREATE PROCEDURE dbo.load_rates AS SELECT * FROM dbo.missing_rates", RuntimeDependencies: runtime}
		desired := migrate.Schema{Definitions: []migrate.Definition{proc}}
		artifact := mustPlanBetween(migrate.Schema{}, desired)
		Expect(statementsOf(artifact)).To(HaveLen(1))
		Expect(artifact.Changes[0].RuntimeDependencies).To(ConsistOf(runtime))
		Expect(mustPlanBetween(desired, desired).Units).To(BeEmpty())
	})

	It("creates dependent views in dependency order and rejects cycles", func() {
		base := migrate.Definition{Name: migrate.Name{Schema: "dbo", Name: "z_base"}, Kind: "VIEW", SQL: "CREATE VIEW dbo.z_base AS SELECT 1 AS id"}
		child := migrate.Definition{Name: migrate.Name{Schema: "dbo", Name: "a_child"}, Kind: "VIEW", SQL: "CREATE VIEW dbo.a_child AS SELECT id FROM dbo.z_base", Dependencies: []migrate.Name{base.Name}}
		statements := statementsOf(mustPlanBetween(migrate.Schema{}, migrate.Schema{Definitions: []migrate.Definition{child, base}}))
		Expect(statements).To(HaveLen(2))
		Expect(statements[0]).To(ContainSubstring("VIEW dbo.z_base"))
		Expect(statements[1]).To(ContainSubstring("VIEW dbo.a_child"))
		base.Dependencies = []migrate.Name{child.Name}
		_, err := planBetween(migrate.Schema{}, migrate.Schema{Definitions: []migrate.Definition{child, base}})
		Expect(err).To(MatchError(ContainSubstring("dependency cycle")))
	})

	It("names a duplicate constraint name across tables and permits ordinary index names repeated on different tables", func() {
		orders := charTable(ordersName, migrate.Column{Name: "id", Type: "int", Default: &migrate.Default{Name: "DF_shared", Expression: "(0)"}})
		audit := charTable(migrate.Name{Schema: "dbo", Name: "audit"}, charColumn("id", "int", false))
		audit.Checks = []migrate.Check{{Name: "DF_shared", Expression: "id >= 0"}}
		_, err := planBetween(migrate.Schema{}, schemaOf(orders, audit))
		Expect(err).To(MatchError(ContainSubstring("duplicate object")))

		indexed := charTable(ordersName, charColumn("id", "int", false))
		indexed.Indexes = []migrate.Index{{Name: "IX_id", Columns: []migrate.IndexColumn{{Name: "id"}}}}
		other := indexed
		other.Name.Name = "audit"
		_, err = planBetween(migrate.Schema{}, schemaOf(indexed, other))
		Expect(err).NotTo(HaveOccurred())
	})
})
