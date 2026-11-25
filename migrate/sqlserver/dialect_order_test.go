package sqlserver_test

import (
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL Server dialect ordering around keys", func() {
	itemsName := migrate.Name{Schema: "dbo", Name: "items"}

	// itemsSchema has a nonclustered primary key, a clustered index that is not
	// the key, and a nonclustered index; withNew adds one more nonclustered index.
	itemsSchema := func(idType string, withNew bool) migrate.Schema {
		t := charTable(itemsName, charColumn("id", idType, false), charColumn("tag", "int", true))
		t.Indexes = []migrate.Index{
			{Name: "PK_items", Primary: true, Unique: true, Constraint: true, Columns: []migrate.IndexColumn{{Name: "id"}}},
			{Name: "IX_items_clu", Clustered: true, Columns: []migrate.IndexColumn{{Name: "id"}}},
			{Name: "IX_items_tag", Columns: []migrate.IndexColumn{{Name: "tag"}}, Included: []string{"id"}},
		}
		if withNew {
			t.Indexes = append(t.Indexes, migrate.Index{Name: "IX_items_new", Columns: []migrate.IndexColumn{{Name: "tag"}}})
		}
		return schemaOf(t)
	}

	It("creates a table's clustered index before its nonclustered ones and drops it after them", func() {
		artifact := planArtifact(completeRequest(itemsSchema("varchar(36)", false), itemsSchema("uniqueidentifier", true)))
		Expect(statementsOf(artifact)).To(Equal([]string{
			"DROP INDEX [IX_items_tag] ON [dbo].[items]",
			"ALTER TABLE [dbo].[items] DROP CONSTRAINT [PK_items]",
			"DROP INDEX [IX_items_clu] ON [dbo].[items]",
			"ALTER TABLE [dbo].[items] ALTER COLUMN [id] uniqueidentifier NOT NULL",
			"CREATE CLUSTERED INDEX [IX_items_clu] ON [dbo].[items] ([id])",
			"CREATE NONCLUSTERED INDEX [IX_items_new] ON [dbo].[items] ([tag])",
			"CREATE NONCLUSTERED INDEX [IX_items_tag] ON [dbo].[items] ([tag]) INCLUDE ([id])",
			"ALTER TABLE [dbo].[items] ADD CONSTRAINT [PK_items] PRIMARY KEY NONCLUSTERED ([id])",
		}))
	})

	It("creates a new foreign key only after the key it references is re-created", func() {
		accountsWith := func(idType string) migrate.Table {
			t := charTable(accounts, charColumn("id", idType, false))
			t.Indexes = []migrate.Index{{Name: systemPKName, Primary: true, Unique: true, Constraint: true, Clustered: true, Columns: []migrate.IndexColumn{{Name: "id"}}}}
			return t
		}
		payments := func(withKey bool) migrate.Table {
			t := charTable(migrate.Name{Schema: "dbo", Name: "payments"}, charColumn("id", "int", false), charColumn("account_id", "uniqueidentifier", false))
			if withKey {
				t.ForeignKeys = []migrate.ForeignKey{{Name: "FK_payments_accounts", Columns: []string{"account_id"}, References: accounts, ReferencedColumns: []string{"id"}}}
			}
			return t
		}
		artifact := planArtifact(completeRequest(
			schemaOf(accountsWith("varchar(36)"), payments(false)),
			schemaOf(accountsWith("uniqueidentifier"), payments(true))))
		Expect(artifact.Units).To(HaveLen(1), "the new key and the key it needs are one unit, so they apply or roll back together")
		Expect(statementsOf(artifact)).To(Equal([]string{
			"ALTER TABLE [dbo].[accounts] DROP CONSTRAINT [PK__accounts__3213E83F8A1B2C3D]",
			"ALTER TABLE [dbo].[accounts] ALTER COLUMN [id] uniqueidentifier NOT NULL",
			"ALTER TABLE [dbo].[accounts] ADD CONSTRAINT [PK__accounts__3213E83F8A1B2C3D] PRIMARY KEY CLUSTERED ([id])",
			"ALTER TABLE [dbo].[payments] ADD CONSTRAINT [FK_payments_accounts] FOREIGN KEY ([account_id]) REFERENCES [dbo].[accounts] ([id]) ON DELETE NO ACTION ON UPDATE NO ACTION",
		}))
	})

	It("plans no ops and no units when a declared scope preserves every change, and the artifact validates", func() {
		current := schemaOf(charTable(charOrders, charColumn("id", "int", false), charColumn("legacy", "int", true)))
		desired := schemaOf(charTable(charOrders, charColumn("id", "int", false)))
		request := completeRequest(current, desired)
		request.Scope = engine.Scope{Mode: engine.ScopeDeclared, Schemas: []string{"dbo"}, Objects: []migrate.Name{charOrders}}

		artifact := planArtifact(request)
		Expect(artifact.Units).To(BeEmpty())
		Expect(artifact.Changes).To(BeEmpty())
		Expect(artifact.Preserved).To(HaveLen(1))
		Expect(engine.Validate(artifact)).To(Succeed())
	})
})
