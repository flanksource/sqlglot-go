package sqlserver_test

import (
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	"github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL Server dialect preconditions", func() {
	const guidLike = "[0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f]-" +
		"[0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f]-[0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f]-" +
		"[0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f]-[0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f]"

	withColumn := func(c migrate.Column) migrate.Schema {
		return schemaOf(charTable(charOrders, charColumn("id", "int", false), c))
	}
	plannedChange := func(current, desired migrate.Schema, id string) migrate.Change {
		diff, err := sqlserver.Planner{}.Diff(current, desired)
		Expect(err).NotTo(HaveOccurred())
		for _, c := range diff.Changes() {
			if c.ID == id {
				return c
			}
		}
		Fail("no change " + id)
		return migrate.Change{}
	}
	guardsFor := func(current, desired migrate.Schema, id string, features map[string]bool) []engine.Precondition {
		guards, err := sqlserver.Planner{}.Preconditions(plannedChange(current, desired, id), current, desired, engine.Capabilities{Features: features})
		Expect(err).NotTo(HaveOccurred())
		return guards
	}
	modifyLabel := changeID("modify", "column", "orders", "label")

	DescribeTable("guards a column conversion with a query that returns an offending row",
		func(from, to migrate.Column, features map[string]bool, expected []engine.Precondition) {
			guards := guardsFor(withColumn(from), withColumn(to), modifyLabel, features)
			if expected == nil {
				Expect(guards).To(BeEmpty())
				return
			}
			Expect(guards).To(Equal(expected))
		},
		Entry("text to uniqueidentifier without TRY_CONVERT checks the canonical shape",
			charColumn("label", "varchar(36)", true), charColumn("label", "uniqueidentifier", true), nil,
			[]engine.Precondition{{
				ID: modifyLabel + "#guid", Change: modifyLabel,
				SQL:     "SELECT TOP (1) 1 FROM [dbo].[orders] WHERE [label] IS NOT NULL AND (LEN([label]) <> 36 OR [label] NOT LIKE '" + guidLike + "')",
				Message: "[dbo].[orders].label holds values that are not canonical GUIDs, which uniqueidentifier would reject",
			}}),
		Entry("text to uniqueidentifier with TRY_CONVERT asks the server",
			charColumn("label", "nvarchar(36)", true), charColumn("label", "uniqueidentifier", true), map[string]bool{sqlserver.FeatureTryConvert: true},
			[]engine.Precondition{{
				ID: modifyLabel + "#guid", Change: modifyLabel,
				SQL:     "SELECT TOP (1) 1 FROM [dbo].[orders] WHERE [label] IS NOT NULL AND (TRY_CONVERT(uniqueidentifier, [label]) IS NULL)",
				Message: "[dbo].[orders].label holds values that are not canonical GUIDs, which uniqueidentifier would reject",
			}}),
		Entry("NULL to NOT NULL looks for a NULL",
			charColumn("label", "int", true), charColumn("label", "int", false), nil,
			[]engine.Precondition{{
				ID: modifyLabel + "#not-null", Change: modifyLabel,
				SQL:     "SELECT TOP (1) 1 FROM [dbo].[orders] WHERE [label] IS NULL",
				Message: "[dbo].[orders].label holds NULL values, which NOT NULL would reject",
			}}),
		Entry("narrowing a varchar measures bytes",
			charColumn("label", "varchar(100)", false), charColumn("label", "varchar(20)", false), nil,
			[]engine.Precondition{{
				ID: modifyLabel + "#narrow", Change: modifyLabel,
				SQL:     "SELECT TOP (1) 1 FROM [dbo].[orders] WHERE DATALENGTH([label]) > 20",
				Message: "[dbo].[orders].label holds values longer than the new length 20",
			}}),
		Entry("narrowing an nvarchar measures characters",
			charColumn("label", "nvarchar(max)", false), charColumn("label", "nvarchar(20)", false), nil,
			[]engine.Precondition{{
				ID: modifyLabel + "#narrow", Change: modifyLabel,
				SQL:     "SELECT TOP (1) 1 FROM [dbo].[orders] WHERE DATALENGTH([label]) / 2 > 20",
				Message: "[dbo].[orders].label holds values longer than the new length 20",
			}}),
		Entry("narrowing a varbinary measures bytes",
			charColumn("label", "varbinary(100)", false), charColumn("label", "varbinary(8)", false), nil,
			[]engine.Precondition{{
				ID: modifyLabel + "#narrow", Change: modifyLabel,
				SQL:     "SELECT TOP (1) 1 FROM [dbo].[orders] WHERE DATALENGTH([label]) > 8",
				Message: "[dbo].[orders].label holds values longer than the new length 8",
			}}),
		Entry("widening needs no guard", charColumn("label", "varchar(20)", false), charColumn("label", "varchar(100)", false), nil, nil),
		Entry("int to bigint needs no guard", charColumn("label", "int", false), charColumn("label", "bigint", false), nil, nil),
		Entry("NOT NULL to NULL needs no guard", charColumn("label", "int", false), charColumn("label", "int", true), nil, nil),
	)

	Describe("uniqueness", func() {
		keyed := func(idx *migrate.Index, columns ...migrate.Column) migrate.Schema {
			t := charTable(charOrders, append([]migrate.Column{charColumn("id", "int", false)}, columns...)...)
			if idx != nil {
				t.Indexes = []migrate.Index{*idx}
			}
			return schemaOf(t)
		}
		label := charColumn("label", "varchar(20)", false)
		region := charColumn("region", "varchar(20)", false)

		DescribeTable("guards a new unique key on an existing table against duplicates",
			func(desired *migrate.Index, kind, member, sql, columns string) {
				current := keyed(nil, label, region)
				id := changeID("add", kind, "orders", member)
				guards := guardsFor(current, keyed(desired, label, region), id, nil)
				Expect(guards).To(HaveLen(1))
				Expect(guards[0].ID).To(Equal(id + "#unique"))
				Expect(guards[0].Change).To(Equal(id))
				Expect(guards[0].SQL).To(Equal(sql))
				Expect(guards[0].Message).To(Equal("[dbo].[orders] holds duplicate values of (" + columns + "), which unique index [" + member + "] would reject"))
			},
			Entry("a unique index",
				&migrate.Index{Name: "UX_label", Unique: true, Columns: []migrate.IndexColumn{{Name: "label"}, {Name: "region"}}}, "index", "UX_label",
				"SELECT TOP (1) 1 FROM [dbo].[orders] GROUP BY [label], [region] HAVING COUNT(*) > 1", "[label], [region]"),
			Entry("a filtered unique index counts only the filtered rows",
				&migrate.Index{Name: "UX_label", Unique: true, Filter: "([region]='eu')", Columns: []migrate.IndexColumn{{Name: "label"}}}, "index", "UX_label",
				"SELECT TOP (1) 1 FROM [dbo].[orders] WHERE ([region]='eu') GROUP BY [label] HAVING COUNT(*) > 1", "[label]"),
			Entry("a unique constraint",
				&migrate.Index{Name: "UQ_label", Unique: true, Constraint: true, Columns: []migrate.IndexColumn{{Name: "label"}}}, "index", "UQ_label",
				"SELECT TOP (1) 1 FROM [dbo].[orders] GROUP BY [label] HAVING COUNT(*) > 1", "[label]"),
			Entry("a primary key",
				&migrate.Index{Name: "PK_orders", Primary: true, Unique: true, Constraint: true, Clustered: true, Columns: []migrate.IndexColumn{{Name: "id"}}}, "primary-key", "PK_orders",
				"SELECT TOP (1) 1 FROM [dbo].[orders] GROUP BY [id] HAVING COUNT(*) > 1", "[id]"),
		)

		It("needs no guard for a non-unique index, a table that is new, or a key over a column the plan adds", func() {
			plain := &migrate.Index{Name: "IX_label", Columns: []migrate.IndexColumn{{Name: "label"}}}
			Expect(guardsFor(keyed(nil, label), keyed(plain, label), changeID("add", "index", "orders", "IX_label"), nil)).To(BeEmpty())

			unique := &migrate.Index{Name: "UX_label", Unique: true, Columns: []migrate.IndexColumn{{Name: "label"}}}
			Expect(guardsFor(migrate.Schema{}, keyed(unique, label), changeID("add", "table", "orders", ""), nil)).To(BeEmpty())

			added := keyed(nil)
			Expect(guardsFor(added, keyed(unique, label), changeID("add", "index", "orders", "UX_label"), nil)).To(BeEmpty())
		})
	})

	It("reports a column that is missing from the schema it should be in, naming it", func() {
		_, err := sqlserver.Planner{}.Preconditions(migrate.Change{ID: "x", Action: "modify", Kind: "column", Object: charOrders, Member: "label"}, migrate.Schema{}, migrate.Schema{}, engine.Capabilities{})
		Expect(err).To(MatchError(ContainSubstring("[dbo].[orders].label is not in the current schema")))
	})
})
