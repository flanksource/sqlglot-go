package migrate

import (
	"ariga.io/atlas/sql/schema"
	native "github.com/flanksource/sqlglot-go/migrate"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL Server bundle defaults", func() {
	It("retains catalog key names and collations and resolves new string columns to the database collation", func() {
		realm, err := sqlServerTables(integrationBundle(), resolveOptions(integrationOptions))
		Expect(err).NotTo(HaveOccurred())
		table := realm.Schemas[0].Tables[0]
		table.AddColumns(schema.NewStringColumn("note", "varchar"))
		current := native.Schema{Tables: []native.Table{{
			Name:    native.Name{Schema: "dbo", Name: "acme_orders"},
			Columns: []native.Column{{Name: "status", Collation: "Latin1_General_100_CI_AS"}},
			Indexes: []native.Index{{Name: "catalog_primary_key", Primary: true}},
		}}}
		resolveSQLServerTableDefaults(table, current, "SQL_Latin1_General_CP1_CI_AS")
		Expect(table.PrimaryKey.Name).To(Equal("catalog_primary_key"))
		Expect(table.Columns[0].Attrs).NotTo(ContainElement(BeAssignableToTypeOf(&schema.Collation{})))
		Expect(table.Columns[1].Attrs).To(ContainElement(&schema.Collation{V: "Latin1_General_100_CI_AS"}))
		Expect(table.Columns[len(table.Columns)-1].Attrs).To(ContainElement(&schema.Collation{V: "SQL_Latin1_General_CP1_CI_AS"}))
	})
})
