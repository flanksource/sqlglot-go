package sqlserver_test

import (
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL Server dialect plans", func() {
	var (
		idOnly       = schemaOf(charTable(charOrders, charColumn("id", "int", false)))
		items        = migrate.Name{Schema: "dbo", Name: "items"}
		addCustomers = charID("add", "table", charCustomers, "")
		addOrders    = charID("add", "table", charOrders, "")
		modifyQty    = charID("modify", "column", charOrders, "qty")
		modifyLabel  = charID("modify", "column", charOrders, "label")
		modifyIndex  = charID("modify", "index", charOrders, "IX_label")
	)

	mutual := func() migrate.Schema {
		orders := charTable(charOrders, charColumn("id", "int", false))
		other := charTable(items, charColumn("id", "int", false))
		orders.ForeignKeys = []migrate.ForeignKey{{Name: "FK_orders_items", Columns: []string{"id"}, References: items, ReferencedColumns: []string{"id"}}}
		other.ForeignKeys = []migrate.ForeignKey{{Name: "FK_items_orders", Columns: []string{"id"}, References: charOrders, ReferencedColumns: []string{"id"}}}
		return schemaOf(orders, other)
	}

	DescribeTable("plans the change IDs and the ordered SQL",
		func(current, desired migrate.Schema, ids []string, statements []string) {
			artifact := mustPlanBetween(current, desired)
			Expect(sortedChangeIDs(artifact)).To(Equal(ids))
			Expect(statementsOf(artifact)).To(Equal(statements))
		},
		Entry("create tables with primary key, index, foreign key, check and default",
			migrate.Schema{}, keyedOrders(),
			[]string{addCustomers, addOrders},
			[]string{
				"CREATE TABLE [dbo].[customers] ([id] int NOT NULL, CONSTRAINT [PK_customers] PRIMARY KEY CLUSTERED ([id]))",
				"CREATE TABLE [dbo].[orders] ([id] int NOT NULL, [customer_id] int NOT NULL, [status] nvarchar(20) NOT NULL CONSTRAINT [DF_orders_status] DEFAULT (N'new'), CONSTRAINT [PK_orders] PRIMARY KEY CLUSTERED ([id]), CONSTRAINT [FK_orders_customers] FOREIGN KEY ([customer_id]) REFERENCES [dbo].[customers] ([id]) ON DELETE NO ACTION ON UPDATE NO ACTION, CONSTRAINT [CK_orders_id] CHECK (([id]>(0))))",
				"CREATE NONCLUSTERED INDEX [IX_orders_customer] ON [dbo].[orders] ([customer_id])",
			}),
		Entry("add a nullable column",
			idOnly, schemaOf(charTable(charOrders, charColumn("id", "int", false), charColumn("note", "nvarchar(50)", true))),
			[]string{charID("add", "column", charOrders, "note")},
			[]string{"ALTER TABLE [dbo].[orders] ADD [note] nvarchar(50) NULL"}),
		Entry("change a column type, dropping and rebinding its default around the ALTER",
			quantityOrders("int"), quantityOrders("bigint"),
			[]string{modifyQty},
			[]string{
				"ALTER TABLE [dbo].[orders] DROP CONSTRAINT [DF_orders_qty]",
				"ALTER TABLE [dbo].[orders] ALTER COLUMN [qty] bigint NOT NULL",
				"ALTER TABLE [dbo].[orders] ADD CONSTRAINT [DF_orders_qty] DEFAULT ((0)) FOR [qty]",
			}),
		Entry("make a nullable column NOT NULL",
			schemaOf(charTable(charOrders, charColumn("id", "int", false), charColumn("label", "nvarchar(20)", true))),
			schemaOf(charTable(charOrders, charColumn("id", "int", false), charColumn("label", "nvarchar(20)", false))),
			[]string{modifyLabel},
			[]string{"ALTER TABLE [dbo].[orders] ALTER COLUMN [label] nvarchar(20) NOT NULL"}),
		Entry("make a NOT NULL column nullable",
			schemaOf(charTable(charOrders, charColumn("id", "int", false), charColumn("label", "nvarchar(20)", false))),
			schemaOf(charTable(charOrders, charColumn("id", "int", false), charColumn("label", "nvarchar(20)", true))),
			[]string{modifyLabel},
			[]string{"ALTER TABLE [dbo].[orders] ALTER COLUMN [label] nvarchar(20) NULL"}),
		Entry("split an index change around a column retyped in the same diff: drop, alter, create",
			labelledOrders(charColumn("label", "nvarchar(20)", true), migrate.Index{Name: "IX_label", Columns: []migrate.IndexColumn{{Name: "label"}}}),
			labelledOrders(charColumn("label", "nvarchar(100)", true), migrate.Index{Name: "IX_label", Unique: true, Columns: []migrate.IndexColumn{{Name: "label"}}}),
			[]string{modifyLabel, modifyIndex},
			[]string{
				"DROP INDEX [IX_label] ON [dbo].[orders]",
				"ALTER TABLE [dbo].[orders] ALTER COLUMN [label] nvarchar(100) NULL",
				"CREATE UNIQUE NONCLUSTERED INDEX [IX_label] ON [dbo].[orders] ([label])",
			}),
		Entry("create a procedure as CREATE OR ALTER",
			migrate.Schema{}, charProcedure("SELECT 1 AS n"),
			[]string{charID("add", "procedure", charLoader, "")},
			[]string{"CREATE OR ALTER PROCEDURE dbo.load_orders AS SELECT 1 AS n"}),
		Entry("edit a procedure body as CREATE OR ALTER",
			charProcedure("SELECT 1 AS n"), charProcedure("SELECT 2 AS n"),
			[]string{charID("modify", "procedure", charLoader, "")},
			[]string{"CREATE OR ALTER PROCEDURE dbo.load_orders AS SELECT 2 AS n"}),
		Entry("plan nothing when current equals desired", keyedOrders(), keyedOrders(), []string{}, []string{}),
		Entry("create mutually referencing tables, adding both foreign keys last",
			migrate.Schema{}, mutual(),
			[]string{charID("add", "table", items, ""), addOrders},
			[]string{
				"CREATE TABLE [dbo].[items] ([id] int NOT NULL)",
				"CREATE TABLE [dbo].[orders] ([id] int NOT NULL)",
				"ALTER TABLE [dbo].[items] ADD CONSTRAINT [FK_items_orders] FOREIGN KEY ([id]) REFERENCES [dbo].[orders] ([id]) ON DELETE NO ACTION ON UPDATE NO ACTION",
				"ALTER TABLE [dbo].[orders] ADD CONSTRAINT [FK_orders_items] FOREIGN KEY ([id]) REFERENCES [dbo].[items] ([id]) ON DELETE NO ACTION ON UPDATE NO ACTION",
			}),
	)

	It("plans a column retype with its index offline as one transactional unit, with no server fingerprint", func() {
		request := completeRequest(
			labelledOrders(charColumn("label", "int", true), migrate.Index{Name: "IX_label", Columns: []migrate.IndexColumn{{Name: "label"}}}),
			labelledOrders(charColumn("label", "bigint", true), migrate.Index{Name: "IX_label", Columns: []migrate.IndexColumn{{Name: "label"}}}),
		)
		request.Capabilities = engine.Capabilities{}
		artifact := planArtifact(request)
		Expect(artifact.Capabilities).To(Equal(engine.Capabilities{TransactionalDDL: true}))
		Expect(artifact.Units).To(HaveLen(1))
		Expect(artifact.Units[0].Transactional).To(BeTrue())
		Expect(statementsOf(artifact)).To(Equal([]string{
			"DROP INDEX [IX_label] ON [dbo].[orders]",
			"ALTER TABLE [dbo].[orders] ALTER COLUMN [label] bigint NULL",
			"CREATE NONCLUSTERED INDEX [IX_label] ON [dbo].[orders] ([label])",
		}))
		Expect(engine.Validate(artifact)).To(Succeed())
	})

	It("runs every drop before any alter and every alter before any create across tables", func() {
		indexed := charTable(charOrders, charColumn("id", "int", false), charColumn("note", "nvarchar(20)", true))
		indexed.Indexes = []migrate.Index{{Name: "IX_note", Columns: []migrate.IndexColumn{{Name: "note"}}}}
		current := schemaOf(charTable(charCustomers, charColumn("id", "int", false)), indexed)
		desired := schemaOf(
			charTable(charCustomers, charColumn("id", "int", false), charColumn("tag", "int", true)),
			charTable(charOrders, charColumn("id", "int", false), charColumn("note", "nvarchar(20)", true)),
			charTable(items, charColumn("id", "int", false)),
		)
		Expect(statementsOf(mustPlanBetween(current, desired))).To(Equal([]string{
			"DROP INDEX [IX_note] ON [dbo].[orders]",
			"ALTER TABLE [dbo].[customers] ADD [tag] int NULL",
			"CREATE TABLE [dbo].[items] ([id] int NOT NULL)",
		}))
	})
})
