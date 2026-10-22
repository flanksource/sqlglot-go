package sqlserver_test

import (
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	"testing"
)

func TestNativePlanner(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Native SQL Server Migrations")
}

var _ = Describe("Native migration contracts", func() {
	It("plans a typed table through the extracted planner", func() {
		plan, err := sqlserver.Plan(migrate.Request{Desired: migrate.Schema{Tables: []migrate.Table{{Name: migrate.Name{Schema: "dbo", Name: "orders"}, Columns: []migrate.Column{{Name: "id", Type: "int"}}}}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Steps).To(HaveLen(1))
		Expect(plan.Steps[0].SQL).To(Equal("CREATE TABLE [dbo].[orders] ([id] int NOT NULL)"))
		Expect(plan.Changes[0].Destructive).To(BeFalse())
	})
	It("marks dropping a target column as destructive", func() {
		current := migrate.Table{Name: migrate.Name{Schema: "dbo", Name: "orders"}, Columns: []migrate.Column{{Name: "id", Type: "int"}, {Name: "note", Type: "nvarchar(20)", Nullable: true}}}
		desired := current
		desired.Columns = desired.Columns[:1]
		plan, err := sqlserver.Plan(migrate.Request{Current: migrate.Schema{Tables: []migrate.Table{current}}, Desired: migrate.Schema{Tables: []migrate.Table{desired}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Changes).To(HaveLen(1))
		Expect(plan.Changes[0]).To(MatchFields(IgnoreExtras, Fields{"Destructive": BeTrue(), "Member": Equal("note")}))
	})
	It("escapes closing brackets in catalog identifiers", func() {
		plan, err := sqlserver.Plan(migrate.Request{Desired: migrate.Schema{Tables: []migrate.Table{{Name: migrate.Name{Schema: "dbo", Name: "odd]orders"}, Columns: []migrate.Column{{Name: "a]b", Type: "int"}}}}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Steps[0].SQL).To(Equal("CREATE TABLE [dbo].[odd]]orders] ([a]]b] int NOT NULL)"))
	})
	It("preserves named defaults, collations and unique constraints", func() {
		table := migrate.Table{Name: migrate.Name{Schema: "dbo", Name: "orders"}, Columns: []migrate.Column{{Name: "code", Type: "nvarchar(20)", Collation: "Latin1_General_100_BIN2", Default: &migrate.Default{Name: "DF_order_code", Expression: "N'new'"}}}, Indexes: []migrate.Index{{Name: "UQ_order_code", Unique: true, Constraint: true, Columns: []migrate.IndexColumn{{Name: "code"}}}}}
		plan, err := sqlserver.Plan(migrate.Request{Desired: migrate.Schema{Tables: []migrate.Table{table}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Steps[0].SQL).To(ContainSubstring("[code] nvarchar(20) COLLATE Latin1_General_100_BIN2 NOT NULL CONSTRAINT [DF_order_code] DEFAULT N'new'"))
		Expect(plan.Steps[1].SQL).To(Equal("ALTER TABLE [dbo].[orders] ADD CONSTRAINT [UQ_order_code] UNIQUE NONCLUSTERED ([code])"))
	})
	It("restores the database collation and preserves the named default", func() {
		current := migrate.Table{Name: migrate.Name{Schema: "dbo", Name: "orders"}, Columns: []migrate.Column{{Name: "code", Type: "nvarchar(20)", Collation: "Latin1_General_100_BIN2", Default: &migrate.Default{Name: "DF_orders_code", Expression: "N'new'"}}}}
		desired := current
		desired.Columns = []migrate.Column{current.Columns[0]}
		desired.Columns[0].Collation = ""
		plan, err := sqlserver.Plan(migrate.Request{Current: migrate.Schema{Tables: []migrate.Table{current}}, Desired: migrate.Schema{Tables: []migrate.Table{desired}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Steps).To(HaveLen(3))
		Expect(plan.Steps[0].SQL).To(Equal("ALTER TABLE [dbo].[orders] DROP CONSTRAINT [DF_orders_code]"))
		Expect(plan.Steps[1].SQL).To(Equal("ALTER TABLE [dbo].[orders] ALTER COLUMN [code] nvarchar(20) COLLATE DATABASE_DEFAULT NOT NULL"))
		Expect(plan.Steps[2].SQL).To(Equal("ALTER TABLE [dbo].[orders] ADD CONSTRAINT [DF_orders_code] DEFAULT N'new' FOR [code]"))
		Expect(plan.Changes[0].Destructive).To(BeTrue())
	})
	It("plans mutual foreign keys after both tables", func() {
		orders := migrate.Table{Name: migrate.Name{Schema: "dbo", Name: "orders"}, Columns: []migrate.Column{{Name: "id", Type: "int"}}}
		items := orders
		items.Name.Name = "items"
		orders.ForeignKeys = []migrate.ForeignKey{{Name: "FK_orders_items", Columns: []string{"id"}, References: items.Name, ReferencedColumns: []string{"id"}}}
		items.ForeignKeys = []migrate.ForeignKey{{Name: "FK_items_orders", Columns: []string{"id"}, References: orders.Name, ReferencedColumns: []string{"id"}}}
		plan, err := sqlserver.Plan(migrate.Request{Desired: migrate.Schema{Tables: []migrate.Table{orders, items}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Steps).To(HaveLen(4))
		Expect(plan.Steps[0].SQL).To(HavePrefix("CREATE TABLE"))
		Expect(plan.Steps[1].SQL).To(HavePrefix("CREATE TABLE"))
		Expect(plan.Steps[2].SQL).To(ContainSubstring("FOREIGN KEY"))
		Expect(plan.Steps[3].SQL).To(ContainSubstring("FOREIGN KEY"))
	})
	It("rejects missing foreign key targets instead of omitting them", func() {
		table := migrate.Table{Name: migrate.Name{Schema: "dbo", Name: "orders"}, Columns: []migrate.Column{{Name: "id", Type: "int"}}, ForeignKeys: []migrate.ForeignKey{{Name: "FK_missing", Columns: []string{"id"}, References: migrate.Name{Schema: "dbo", Name: "missing"}, ReferencedColumns: []string{"id"}}}}
		_, err := sqlserver.Plan(migrate.Request{Desired: migrate.Schema{Tables: []migrate.Table{table}}})
		Expect(err).To(MatchError(ContainSubstring("missing referenced table missing")))
	})
	It("plans full view and trigger definitions in their own batches", func() {
		table := migrate.Table{Name: migrate.Name{Schema: "dbo", Name: "orders"}, Columns: []migrate.Column{{Name: "id", Type: "int"}}}
		view := migrate.Definition{Name: migrate.Name{Schema: "dbo", Name: "order_ids"}, Kind: "VIEW", SQL: "CREATE VIEW dbo.order_ids AS SELECT id FROM dbo.orders"}
		trigger := migrate.Definition{Name: migrate.Name{Schema: "dbo", Name: "audit_orders"}, Kind: "TRIGGER", Parent: &table.Name, SQL: "CREATE TRIGGER dbo.audit_orders ON dbo.orders AFTER INSERT AS BEGIN SELECT 1; END"}
		plan, err := sqlserver.Plan(migrate.Request{Current: migrate.Schema{Tables: []migrate.Table{table}}, Desired: migrate.Schema{Tables: []migrate.Table{table}, Definitions: []migrate.Definition{view, trigger}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Steps).To(HaveLen(3))
		Expect(plan.Steps).To(ContainElement(MatchFields(IgnoreExtras, Fields{"SQL": Equal("ENABLE TRIGGER [dbo].[audit_orders] ON [dbo].[orders]")})))
		Expect(plan.Steps).To(ContainElement(MatchFields(IgnoreExtras, Fields{"SQL": Equal("CREATE OR ALTER VIEW dbo.order_ids AS SELECT id FROM dbo.orders")})))
		Expect(plan.Steps).To(ContainElement(MatchFields(IgnoreExtras, Fields{"SQL": Equal("CREATE OR ALTER TRIGGER dbo.audit_orders ON dbo.orders AFTER INSERT AS BEGIN SELECT 1; END")})))
	})
	It("creates dependent views in dependency order and rejects cycles", func() {
		base := migrate.Definition{Name: migrate.Name{Schema: "dbo", Name: "z_base"}, Kind: "VIEW", SQL: "CREATE VIEW dbo.z_base AS SELECT 1 AS id"}
		child := migrate.Definition{Name: migrate.Name{Schema: "dbo", Name: "a_child"}, Kind: "VIEW", SQL: "CREATE VIEW dbo.a_child AS SELECT id FROM dbo.z_base", Dependencies: []migrate.Name{base.Name}}
		plan, err := sqlserver.Plan(migrate.Request{Desired: migrate.Schema{Definitions: []migrate.Definition{child, base}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Steps[0].SQL).To(ContainSubstring("VIEW dbo.z_base"))
		Expect(plan.Steps[1].SQL).To(ContainSubstring("VIEW dbo.a_child"))
		base.Dependencies = []migrate.Name{child.Name}
		_, err = sqlserver.Plan(migrate.Request{Desired: migrate.Schema{Definitions: []migrate.Definition{child, base}}})
		Expect(err).To(MatchError(ContainSubstring("dependency cycle")))
	})
})
