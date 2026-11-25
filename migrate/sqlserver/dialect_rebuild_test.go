package sqlserver_test

import (
	"encoding/json"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	accounts       = migrate.Name{Schema: "dbo", Name: "accounts"}
	systemPKName   = "PK__accounts__3213E83F8A1B2C3D"
	accountIDRef   = engine.ColumnRef{Table: accounts, Column: "id"}
	orderAccountID = engine.ColumnRef{Table: charOrders, Column: "account_id"}
)

// guidSchema is accounts and orders with their ids typed varchar(36) or
// uniqueidentifier; ordersType types the child column of the foreign key.
func guidSchema(accountsType, ordersType string) migrate.Schema {
	id := charColumn("id", accountsType, false)
	id.Default = &migrate.Default{Name: "DF_accounts_id", Expression: "(CONVERT([varchar](36),newid()))"}
	if accountsType == "uniqueidentifier" {
		id.Default.Expression = "(newid())"
	}
	acc := charTable(accounts, id, charColumn("note", "nvarchar(50)", true))
	acc.Indexes = []migrate.Index{
		{Name: systemPKName, Primary: true, Unique: true, Constraint: true, Clustered: true, Columns: []migrate.IndexColumn{{Name: "id"}}},
		{Name: "IX_accounts_id", Columns: []migrate.IndexColumn{{Name: "id"}}, Included: []string{"note"}, Filter: "([note] IS NOT NULL)"},
		{Name: "IX_accounts_note", Columns: []migrate.IndexColumn{{Name: "note"}}, Filter: "([id] IS NOT NULL)"},
	}
	acc.Checks = []migrate.Check{{Name: "CK_accounts_id", Expression: "([id] IS NOT NULL)"}}
	orders := charTable(charOrders, charColumn("id", "int", false), charColumn("account_id", ordersType, false))
	orders.ForeignKeys = []migrate.ForeignKey{{Name: "FK_orders_accounts", Columns: []string{"account_id"}, References: accounts, ReferencedColumns: []string{"id"}, OnDelete: "CASCADE"}}
	return schemaOf(acc, orders)
}

// liveDependents are what the database half reads and the offline schema
// cannot hold: a user statistic, an index with storage options and a
// system-named primary key with a fill factor.
func liveDependents() []engine.Dependent {
	on := []engine.ColumnRef{accountIDRef}
	return []engine.Dependent{
		{Kind: engine.DependentStatistic, Table: accounts, Name: "ST_accounts_id", On: on, Definition: "CREATE STATISTICS [ST_accounts_id] ON [dbo].[accounts] ([id])"},
		{Kind: engine.DependentIndex, Table: accounts, Name: "IX_accounts_note", On: on, Index: &migrate.Index{
			Name: "IX_accounts_note", Columns: []migrate.IndexColumn{{Name: "note", Descending: true}}, Filter: "([id] IS NOT NULL)",
			FillFactor: 80, PadIndex: true, Compression: "PAGE", Filegroup: "FG_ARCHIVE",
		}},
		{Kind: engine.DependentPrimaryKey, Table: accounts, Name: systemPKName, On: on, Index: &migrate.Index{
			Name: systemPKName, Primary: true, Unique: true, Constraint: true, Clustered: true, Columns: []migrate.IndexColumn{{Name: "id"}}, FillFactor: 90, Compression: "ROW",
		}},
	}
}

var _ = Describe("SQL Server dialect forced rebuilds", func() {
	It("drops and re-creates every dependent of a column retyped from varchar(36) to uniqueidentifier", func() {
		request := completeRequest(guidSchema("varchar(36)", "varchar(36)"), guidSchema("uniqueidentifier", "uniqueidentifier"))
		request.Dependents = liveDependents()
		artifact := planArtifact(request)

		Expect(statementsOf(artifact)).To(Equal([]string{
			"ALTER TABLE [dbo].[accounts] DROP CONSTRAINT [CK_accounts_id]",
			"ALTER TABLE [dbo].[orders] DROP CONSTRAINT [FK_orders_accounts]",
			"DROP INDEX [IX_accounts_id] ON [dbo].[accounts]",
			"DROP INDEX [IX_accounts_note] ON [dbo].[accounts]",
			"ALTER TABLE [dbo].[accounts] DROP CONSTRAINT [PK__accounts__3213E83F8A1B2C3D]",
			"DROP STATISTICS [dbo].[accounts].[ST_accounts_id]",
			"ALTER TABLE [dbo].[accounts] DROP CONSTRAINT [DF_accounts_id]",
			"ALTER TABLE [dbo].[accounts] ALTER COLUMN [id] uniqueidentifier NOT NULL",
			"ALTER TABLE [dbo].[accounts] ADD CONSTRAINT [DF_accounts_id] DEFAULT (newid()) FOR [id]",
			"ALTER TABLE [dbo].[orders] ALTER COLUMN [account_id] uniqueidentifier NOT NULL",
			"ALTER TABLE [dbo].[accounts] WITH CHECK ADD CONSTRAINT [CK_accounts_id] CHECK (([id] IS NOT NULL))",
			"ALTER TABLE [dbo].[accounts] CHECK CONSTRAINT [CK_accounts_id]",
			"ALTER TABLE [dbo].[accounts] ADD CONSTRAINT [PK__accounts__3213E83F8A1B2C3D] PRIMARY KEY CLUSTERED ([id]) WITH (FILLFACTOR = 90, DATA_COMPRESSION = ROW)",
			"ALTER TABLE [dbo].[orders] WITH CHECK ADD CONSTRAINT [FK_orders_accounts] FOREIGN KEY ([account_id]) REFERENCES [dbo].[accounts] ([id]) ON DELETE CASCADE",
			"ALTER TABLE [dbo].[orders] CHECK CONSTRAINT [FK_orders_accounts]",
			"CREATE NONCLUSTERED INDEX [IX_accounts_id] ON [dbo].[accounts] ([id]) INCLUDE ([note]) WHERE ([note] IS NOT NULL)",
			"CREATE NONCLUSTERED INDEX [IX_accounts_note] ON [dbo].[accounts] ([note] DESC) WHERE ([id] IS NOT NULL) WITH (PAD_INDEX = ON, FILLFACTOR = 80, DATA_COMPRESSION = PAGE) ON [FG_ARCHIVE]",
			"CREATE STATISTICS [ST_accounts_id] ON [dbo].[accounts] ([id])",
		}))
	})

	It("names the change that forced each rebuild", func() {
		request := completeRequest(guidSchema("varchar(36)", "varchar(36)"), guidSchema("uniqueidentifier", "uniqueidentifier"))
		request.Dependents = liveDependents()
		artifact := planArtifact(request)

		modifyID := changeID("modify", "column", "accounts", "id")
		modifyOrdersID := changeID("modify", "column", "orders", "account_id")
		rebuilt := map[string]string{}
		for _, c := range artifact.Changes {
			if c.Action == "rebuild" {
				rebuilt[c.Member] = c.Cause
				Expect(c.Requires).NotTo(BeEmpty())
			}
		}
		Expect(rebuilt).To(Equal(map[string]string{
			"CK_accounts_id": modifyID, "IX_accounts_id": modifyID, "IX_accounts_note": modifyID, "ST_accounts_id": modifyID,
			systemPKName: modifyID, "FK_orders_accounts": min(modifyID, modifyOrdersID),
		}))
		Expect(artifact.Dependents).To(HaveLen(6))
	})

	It("refuses an inbound foreign key whose child column is not retyped in the same plan", func() {
		_, err := engine.Plan(completeRequest(guidSchema("varchar(36)", "varchar(36)"), guidSchema("uniqueidentifier", "varchar(36)")))
		Expect(err).To(MatchError(And(ContainSubstring("FK_orders_accounts"), ContainSubstring("dbo.orders.account_id"), ContainSubstring("same plan"))))
	})

	It("plans an artifact that validates and refuses an edited statement", func() {
		request := completeRequest(guidSchema("varchar(36)", "varchar(36)"), guidSchema("uniqueidentifier", "uniqueidentifier"))
		request.Dependents = liveDependents()
		artifact := planArtifact(request)
		Expect(engine.Validate(artifact)).To(Succeed())

		body, err := json.Marshal(artifact)
		Expect(err).NotTo(HaveOccurred())
		var edited engine.Artifact
		Expect(json.Unmarshal(body, &edited)).To(Succeed())
		edited.Units[0].Ops[0].Statements[0].SQL += " -- edited"
		Expect(engine.Validate(edited)).To(MatchError(ContainSubstring("hash mismatch")))
	})

	It("keeps the preconditions of the retype in the unit that holds its rebuilds", func() {
		request := completeRequest(guidSchema("varchar(36)", "varchar(36)"), guidSchema("uniqueidentifier", "uniqueidentifier"))
		artifact := planArtifact(request)

		Expect(artifact.Units).To(HaveLen(1))
		ids := []string{}
		for _, p := range artifact.Units[0].Preconditions {
			ids = append(ids, p.ID)
		}
		Expect(ids).To(ConsistOf(
			changeID("modify", "column", "accounts", "id")+"#guid",
			changeID("modify", "column", "orders", "account_id")+"#guid",
		))
	})
})
