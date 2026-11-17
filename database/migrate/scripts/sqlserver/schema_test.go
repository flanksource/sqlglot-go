package sqlserver

import (
	catalog "github.com/flanksource/sqlglot-go/database/inspect/catalog/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Schema dependency projections", func() {
	It("includes all columns in a composite foreign key that constrains the selected column", func() {
		key := []catalog.ForeignKey{
			{Name: "FK_region", Column: "tenant_id", RefColumn: "tenant_id"},
			{Name: "FK_region", Column: "region_id", RefColumn: "id"},
		}
		snapshot := schemaSnapshot{
			Columns:     []catalog.Column{{Name: "tenant_id"}, {Name: "region_id"}},
			ForeignKeys: append(append([]catalog.ForeignKey{}, key...), catalog.ForeignKey{Name: "FK_other", Column: "region_id"}),
		}
		projected, err := snapshot.column("tenant_id")
		Expect(err).NotTo(HaveOccurred())
		Expect(projected.ForeignKeys).To(Equal(key))
	})
	It("ignores unrelated columns in a column dependency but includes its constraints", func() {
		base := schemaSnapshot{Columns: []catalog.Column{{Name: "country", DataType: "nvarchar", Position: 1}}, Indexes: []catalog.Index{{Name: "country_key", KeyColumns: []string{"country"}, Unique: true}}, IndexColumns: []catalog.IndexColumn{{Index: "country_key", Column: "country", KeyOrdinal: 1}}}
		want := base
		actual := base
		actual.Columns = append([]catalog.Column{{Name: "unrelated", DataType: "int", Position: 1}}, base.Columns...)
		actual.Columns[1].Position = 2
		projected, err := actual.column("country")
		Expect(err).NotTo(HaveOccurred())
		expected, err := want.column("country")
		Expect(err).NotTo(HaveOccurred())
		Expect(projected).To(Equal(expected))
		actual.Indexes = []catalog.Index{{Name: "country_key", KeyColumns: []string{"country"}, Unique: false}}
		changed, err := actual.column("country")
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).NotTo(Equal(expected))
	})
})
