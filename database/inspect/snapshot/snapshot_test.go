package snapshot

import (
	"database/sql"
	"testing"

	"github.com/flanksource/sqlglot-go/database/inspect/catalog/sqlserver"
	"github.com/flanksource/sqlglot-go/migrate"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSnapshot(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SQL Server migration snapshots")
}

func fixture() catalog {
	return catalog{schema: "dbo", tables: []mssqlcatalog.Table{{Schema: "dbo", Name: "orders", Type: "BASE TABLE"}},
		columns: []mssqlcatalog.Column{
			{Table: "orders", Name: "id", DataType: "int", MaxLength: new(4), Precision: new(10), Scale: new(0), Identity: true, IdentitySeed: new(int64(100)), IdentityIncrement: new(int64(5))},
			{Table: "orders", Name: "label", DataType: "nvarchar", MaxLength: new(40), Precision: new(0), Scale: new(0), Default: new("(N'pending')"), DefaultConstraint: new("DF_label")},
		}, indexes: []mssqlcatalog.Index{{Table: "orders", Name: "IX_label", TypeDesc: "NONCLUSTERED", Filter: "[label] IS NOT NULL"}},
		parts:      []mssqlcatalog.IndexColumn{{Table: "orders", Index: "IX_label", Column: "label", KeyOrdinal: 1, Descending: true}, {Table: "orders", Index: "IX_label", Column: "id", Included: true}},
		collations: map[[2]string]string{{"orders", "label"}: "Latin1_General_CI_AS"},
	}
}

var _ = Describe("Complete catalogue projection", func() {
	It("preserves identity, named defaults, collation and index parts", func() {
		schema, err := build(fixture())
		Expect(err).NotTo(HaveOccurred())
		Expect(schema.Tables).To(Equal([]migrate.Table{{Name: migrate.Name{Schema: "dbo", Name: "orders"},
			Columns: []migrate.Column{{Name: "id", Type: "int", Identity: &migrate.Identity{Seed: 100, Increment: 5}}, {Name: "label", Type: "nvarchar(20)", Default: &migrate.Default{Name: "DF_label", Expression: "(N'pending')"}, Collation: "Latin1_General_CI_AS"}},
			Indexes: []migrate.Index{{Name: "IX_label", Columns: []migrate.IndexColumn{{Name: "label", Descending: true}}, Included: []string{"id"}, Filter: "[label] IS NOT NULL"}},
		}}))
	})

	It("projects index fill factor and the ROWGUIDCOL column", func() {
		c := fixture()
		c.fillFactors = map[[2]string]int{{"orders", "IX_label"}: 80}
		c.rowGUIDs = map[[2]string]bool{{"orders", "label"}: true}
		schema, err := build(c)
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{schema.Tables[0].Indexes[0].FillFactor, schema.Tables[0].Columns[0].RowGUID, schema.Tables[0].Columns[1].RowGUID}).To(Equal([]any{80, false, true}))
	})

	DescribeTable("refuses definitions that cannot be reproduced",
		func(change func(*catalog), message string) {
			c := fixture()
			change(&c)
			_, err := build(c)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("computed column", func(c *catalog) { c.columns[0].Computed = true }, "computed"),
		Entry("user-defined type", func(c *catalog) { c.columns[0].UserDefinedType = true }, "user-defined"),
		Entry("missing identity seed", func(c *catalog) { c.columns[0].IdentitySeed = nil }, "identity"),
		Entry("unnamed default", func(c *catalog) { c.columns[1].DefaultConstraint = nil }, "constraint name"),
		Entry("disabled check", func(c *catalog) {
			c.checks = []mssqlcatalog.CheckConstraint{{Table: "orders", Name: "CK_id", Definition: "id > 0", Disabled: true}}
		}, "disabled check"),
		Entry("encrypted routine", func(c *catalog) {
			c.routines = []mssqlcatalog.Routine{{Schema: "dbo", Name: "update_orders", Type: mssqlcatalog.Procedure}}
		}, "definition"),
	)

	text := func(value string) sql.NullString { return sql.NullString{String: value, Valid: true} }
	objectID := sql.NullInt64{Int64: 42, Valid: true}
	DescribeTable("classifies module dependencies",
		func(row dependencyRow, local *migrate.Name, runtime *migrate.RuntimeDependency) {
			name, dispatched, isLocal := classifyDependency(row)
			if local != nil {
				Expect([]any{name, isLocal}).To(Equal([]any{*local, true}))
				return
			}
			Expect([]any{dispatched, isLocal}).To(Equal([]any{*runtime, false}))
		},
		Entry("resolved local object", dependencyRow{Schema: text("dbo"), Entity: "orders", ID: objectID},
			&migrate.Name{Schema: "dbo", Name: "orders"}, nil),
		Entry("missing object in a known schema", dependencyRow{Schema: text("dbo"), Entity: "missing_rates"},
			nil, &migrate.RuntimeDependency{Schema: "dbo", Name: "missing_rates"}),
		Entry("built-in function without a schema", dependencyRow{Entity: "OPENJSON"},
			nil, &migrate.RuntimeDependency{Name: "OPENJSON"}),
		Entry("XML method call recorded as a three-part name", dependencyRow{Database: text("s"), Schema: text("n"), Entity: "value"},
			nil, &migrate.RuntimeDependency{Database: "s", Schema: "n", Name: "value"}),
		Entry("linked server reference", dependencyRow{Server: text("remote"), Database: text("archive"), Schema: text("dbo"), Entity: "orders"},
			nil, &migrate.RuntimeDependency{Server: "remote", Database: "archive", Schema: "dbo", Name: "orders"}),
	)
})
