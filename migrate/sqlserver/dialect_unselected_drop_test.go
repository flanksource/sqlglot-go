package sqlserver_test

import (
	"github.com/flanksource/sqlglot-go/migrate"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// An unreviewed apply selects every planned change but the drops of undeclared
// columns. The old key of a table is dropped whatever form it has and whichever
// columns it covers, including the column that stays.
var _ = Describe("SQL Server old keys dropped while an undeclared column stays", func() {
	const oldKey = "UQ_acme_documents_old"
	keptColumn := changeID("drop", "column", "orders", "legacy_note")
	oldKeyDrop := changeID("drop", "index", "orders", oldKey)

	DescribeTable("lowers the old key's drop to its own statement and leaves the column's drop out",
		func(constraint bool, columns []string, statement string) {
			key := migrate.Index{Name: oldKey, Unique: true, Constraint: constraint}
			for _, c := range columns {
				key.Columns = append(key.Columns, migrate.IndexColumn{Name: c})
			}
			live := charTable(charOrders, charColumn("id", "int", false), charColumn("input_hash", "nvarchar(64)", false), charColumn("legacy_note", "nvarchar(50)", true))
			live.Indexes = []migrate.Index{key}
			desired := charTable(charOrders, charColumn("id", "int", false), charColumn("input_hash", "nvarchar(64)", false), charColumn("content_digest", "nvarchar(64)", true))
			desired.Indexes = []migrate.Index{{Name: "UQ_acme_documents_digest", Unique: true, Constraint: true, Columns: []migrate.IndexColumn{{Name: "id"}, {Name: "content_digest"}}}}

			full := mustPlanBetween(schemaOf(live), schemaOf(desired))
			var kept []string
			for _, c := range full.Changes {
				if c.Action != "drop" || c.Kind != "column" {
					kept = append(kept, c.ID)
				}
			}
			Expect(kept).To(HaveLen(len(full.Changes) - 1))

			a := mustPlanBetween(schemaOf(live), schemaOf(desired), kept...)
			Expect(sortedChangeIDs(a)).To(ContainElement(oldKeyDrop))
			Expect(sortedChangeIDs(a)).NotTo(ContainElement(keptColumn))
			Expect(statementsOf(a)).To(ContainElement(statement))
		},
		Entry("a unique index over declared columns", false, []string{"id", "input_hash"}, "DROP INDEX [UQ_acme_documents_old] ON [dbo].[orders]"),
		Entry("a unique index over the undeclared column", false, []string{"id", "legacy_note"}, "DROP INDEX [UQ_acme_documents_old] ON [dbo].[orders]"),
		Entry("a unique constraint over declared columns", true, []string{"id", "input_hash"}, "ALTER TABLE [dbo].[orders] DROP CONSTRAINT [UQ_acme_documents_old]"),
		Entry("a unique constraint over the undeclared column", true, []string{"id", "legacy_note"}, "ALTER TABLE [dbo].[orders] DROP CONSTRAINT [UQ_acme_documents_old]"),
	)
})
