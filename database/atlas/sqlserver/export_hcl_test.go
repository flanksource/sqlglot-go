package sqlserver_test

import (
	"slices"
	"strings"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/database/atlas/sqlserver"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	native "github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// exportPlanner is the pure SQL Server planner registered for these specs,
// because the database half owns "sqlserver" and imports this package's callers.
type exportPlanner struct{ native.Planner }

func (exportPlanner) Name() string { return "sqlserver-export-test" }

func init() { engine.Register(exportPlanner{}) }

var (
	customers = migrate.Name{Schema: "dbo", Name: "customers"}
	orders    = migrate.Name{Schema: "dbo", Name: "orders"}
)

// exportedSchema holds, between customers and orders, everything the export
// must carry: a nonclustered named primary key, a clustered non-key index, a
// descending key with INCLUDE and a filter, a unique constraint, identity, named
// and system-named defaults, a collation, a foreign key with actions and a check.
func exportedSchema() migrate.Schema {
	customersTable := migrate.Table{
		Name: customers,
		Columns: []migrate.Column{
			{Name: "id", Type: "int", Identity: &migrate.Identity{Seed: 1, Increment: 1}},
			{Name: "region", Type: "nvarchar(10)", Nullable: true},
		},
		Indexes: []migrate.Index{{Name: "PK_customers", Primary: true, Unique: true, Columns: []migrate.IndexColumn{{Name: "id"}}}},
	}
	ordersTable := migrate.Table{
		Name: orders,
		Columns: []migrate.Column{
			{Name: "id", Type: "int"},
			{Name: "customer_id", Type: "int"},
			{Name: "status", Type: "nvarchar(20)", Default: &migrate.Default{Name: "DF_orders_status", Expression: "(N'new')"}},
			{Name: "qty", Type: "int", Default: &migrate.Default{Name: "DF__orders__qty__1A2B3C4D", Expression: "((0))", SystemNamed: true}},
			{Name: "note", Type: "nvarchar(max)", Nullable: true},
			{Name: "amount", Type: "decimal(18,2)", Nullable: true},
			{Name: "label", Type: "nvarchar(50)", Nullable: true, Collation: "Latin1_General_BIN"},
		},
		Indexes: []migrate.Index{
			{Name: "PK_orders", Primary: true, Unique: true, Columns: []migrate.IndexColumn{{Name: "id"}}},
			{Name: "CIX_orders_customer", Clustered: true, Columns: []migrate.IndexColumn{{Name: "customer_id"}, {Name: "id"}}},
			{Name: "IX_orders_status", Columns: []migrate.IndexColumn{{Name: "status"}, {Name: "qty", Descending: true}},
				Included: []string{"amount", "note"}, Filter: "([qty]>(0))"},
			{Name: "UQ_orders_label", Unique: true, Constraint: true, Columns: []migrate.IndexColumn{{Name: "label"}}},
		},
		ForeignKeys: []migrate.ForeignKey{{Name: "FK_orders_customers", Columns: []string{"customer_id"}, References: customers,
			ReferencedColumns: []string{"id"}, OnDelete: "CASCADE", OnUpdate: "NO ACTION"}},
		Checks: []migrate.Check{{Name: "CK_orders_qty", Expression: "([qty]>=(0))"}},
	}
	return migrate.Schema{Tables: []migrate.Table{customersTable, ordersTable}}
}

// exportOptions are the options the specs share: the database default collation
// is not the one of the label column.
var exportOptions = native.HCLOptions{DatabaseCollation: "SQL_Latin1_General_CP1_CI_AS"}

func evaluate(content ...string) migrate.Schema {
	GinkgoHelper()
	var realm schema.Realm
	Expect(sqlserver.EvalHCLBytes([]byte(strings.Join(content, "\n")), &realm, nil)).To(Succeed())
	out, err := native.FromAtlas(&realm)
	Expect(err).NotTo(HaveOccurred())
	return out
}

func export(s migrate.Schema, opts native.HCLOptions) []native.HCLFile {
	GinkgoHelper()
	files, err := native.ExportHCL(s, opts)
	Expect(err).NotTo(HaveOccurred())
	return files
}

// canonical orders the members the way a schema read back lists them and drops
// the names SQL Server generated, which differ in every database.
func canonical(s migrate.Schema) migrate.Schema {
	out := migrate.Schema{Tables: slices.Clone(s.Tables)}
	for i, t := range out.Tables {
		t.Indexes = slices.Clone(t.Indexes)
		slices.SortFunc(t.Indexes, func(a, b migrate.Index) int { return strings.Compare(a.Name, b.Name) })
		t.Columns = slices.Clone(t.Columns)
		for j, c := range t.Columns {
			if c.Default != nil && c.Default.SystemNamed {
				c.Default = &migrate.Default{Expression: c.Default.Expression}
			}
			t.Columns[j] = c
		}
		out.Tables[i] = t
	}
	slices.SortFunc(out.Tables, func(a, b migrate.Table) int { return strings.Compare(a.Name.Name, b.Name.Name) })
	return out
}

func planBetween(current, desired migrate.Schema) engine.Artifact {
	GinkgoHelper()
	artifact, err := engine.Plan(engine.PlanRequest{
		Dialect: "sqlserver-export-test", Scope: engine.Scope{Mode: engine.ScopeComplete, Schemas: []string{"dbo"}},
		Current: current, Desired: desired,
	})
	Expect(err).NotTo(HaveOccurred())
	return artifact
}

var _ = Describe("exporting SQL Server HCL", func() {
	It("reads back, through the evaluator, as the same schema with no change to plan", func() {
		original := exportedSchema()
		files := export(original, exportOptions)
		Expect(files).To(HaveLen(1))
		roundTripped := evaluate(files[0].Content)
		Expect(canonical(roundTripped)).To(Equal(canonical(original)))
		Expect(planBetween(original, roundTripped).Changes).To(BeEmpty())
	})

	It("writes a collation only where it differs from the database default", func() {
		files := export(exportedSchema(), exportOptions)
		Expect(strings.Count(files[0].Content, "collate")).To(Equal(1))
		Expect(files[0].Content).To(ContainSubstring(`collate = "Latin1_General_BIN"`))
		all := export(exportedSchema(), native.HCLOptions{})
		Expect(evaluate(all[0].Content).Tables[1].Columns[6].Collation).To(Equal("Latin1_General_BIN"))
	})

	DescribeTable("round-trips each column type",
		func(typ string) {
			original := migrate.Schema{Tables: []migrate.Table{{Name: orders, Columns: []migrate.Column{{Name: "id", Type: "int"}, {Name: "c", Type: typ, Nullable: true}}}}}
			Expect(evaluate(export(original, exportOptions)[0].Content)).To(Equal(original))
		},
		Entry("bit", "bit"), Entry("tinyint", "tinyint"), Entry("smallint", "smallint"), Entry("bigint", "bigint"),
		Entry("decimal", "decimal(18,2)"), Entry("numeric", "numeric(10,0)"), Entry("money", "money"), Entry("smallmoney", "smallmoney"),
		Entry("real", "real"), Entry("float", "float"), Entry("char", "char(10)"), Entry("varchar", "varchar(50)"),
		Entry("nchar", "nchar(5)"), Entry("varchar(max)", "varchar(max)"), Entry("nvarchar(max)", "nvarchar(max)"),
		Entry("varbinary(max)", "varbinary(max)"), Entry("binary", "binary(16)"), Entry("text", "text"), Entry("ntext", "ntext"),
		Entry("image", "image"), Entry("date", "date"), Entry("time", "time(7)"), Entry("datetime", "datetime"),
		Entry("datetime2", "datetime2(3)"), Entry("smalldatetime", "smalldatetime"), Entry("datetimeoffset", "datetimeoffset(7)"),
		Entry("uniqueidentifier", "uniqueidentifier"), Entry("xml", "xml"), Entry("sql_variant", "sql_variant"),
	)

	It("escapes quotes, backslashes, newlines and template openers in expressions", func() {
		expression := "([a]<>'\"x\"\\y' AND [b] <> '${z} %{w}'\n)"
		original := migrate.Schema{Tables: []migrate.Table{{
			Name: orders, Columns: []migrate.Column{{Name: "a", Type: "int"}, {Name: "b", Type: "int"}},
			Checks: []migrate.Check{{Name: "CK_a", Expression: expression}},
		}}}
		Expect(evaluate(export(original, exportOptions)[0].Content).Tables[0].Checks).To(Equal(original.Tables[0].Checks))
	})

	It("writes a column or table name that is not an identifier as an index", func() {
		parent := migrate.Table{Name: migrate.Name{Schema: "dbo", Name: "my parent"}, Columns: []migrate.Column{{Name: "the id", Type: "int"}},
			Indexes: []migrate.Index{{Name: "PK_p", Primary: true, Unique: true, Columns: []migrate.IndexColumn{{Name: "the id"}}}}}
		child := migrate.Table{Name: migrate.Name{Schema: "dbo", Name: "child"}, Columns: []migrate.Column{{Name: "pid", Type: "int"}},
			ForeignKeys: []migrate.ForeignKey{{Name: "FK_c", Columns: []string{"pid"}, References: parent.Name, ReferencedColumns: []string{"the id"}, OnDelete: "NO ACTION", OnUpdate: "NO ACTION"}}}
		original := migrate.Schema{Tables: []migrate.Table{parent, child}}
		Expect(canonical(evaluate(export(original, exportOptions)[0].Content))).To(Equal(canonical(original)))
	})

	It("writes one file per table, each declaring its schema, that evaluate together to the same schema", func() {
		files := export(exportedSchema(), native.HCLOptions{Layout: native.HCLFilePerTable, DatabaseCollation: exportOptions.DatabaseCollation})
		names := []string{}
		for _, f := range files {
			names = append(names, f.Name)
			Expect(f.Content).To(HavePrefix("schema \"dbo\" {}\n"))
		}
		Expect(names).To(Equal([]string{"customers.hcl", "orders.hcl"}))
	})

	Describe("what the HCL cannot declare", func() {
		unsupported := func() migrate.Schema {
			s := exportedSchema()
			s.Tables[1].Columns[0].RowGUID = true
			s.Tables[1].Indexes[2].FillFactor = 80
			s.Tables[1].Indexes[3].Compression = "PAGE"
			s.Tables[1].ForeignKeys = append(s.Tables[1].ForeignKeys, migrate.ForeignKey{Name: "FK_orders_elsewhere", Columns: []string{"id"},
				References: migrate.Name{Schema: "dbo", Name: "elsewhere"}, ReferencedColumns: []string{"id"}})
			s.Definitions = []migrate.Definition{{Name: migrate.Name{Schema: "dbo", Name: "load"}, Kind: "PROCEDURE"}}
			return s
		}

		It("fails naming the table, the member and the attribute of each", func() {
			_, err := native.ExportHCL(unsupported(), exportOptions)
			Expect(err).To(HaveOccurred())
			for _, want := range []string{
				"table dbo.orders, column id: ROWGUIDCOL",
				"table dbo.orders, index IX_orders_status: fill factor 80",
				"table dbo.orders, index UQ_orders_label: data compression PAGE",
				"table dbo.orders, foreign key FK_orders_elsewhere: reference to dbo.elsewhere",
				"dbo.load is not a table",
			} {
				Expect(err.Error()).To(ContainSubstring(want))
			}
		})

		It("leaves them out with one comment line each, and what remains evaluates", func() {
			files := export(unsupported(), native.HCLOptions{OmitUnsupported: true, DatabaseCollation: exportOptions.DatabaseCollation})
			Expect(strings.Count(files[0].Content, "# omitted:")).To(Equal(5))
			remaining := evaluate(files[0].Content)
			Expect(remaining.Tables[1].ForeignKeys).To(HaveLen(1))
			Expect(remaining.Tables[1].Indexes).To(HaveLen(4))
		})
	})

	It("declares only the named columns of a table, partial, with no keys, indexes or checks", func() {
		files := export(exportedSchema(), native.HCLOptions{Columns: map[string][]string{"orders": {"status", "qty"}}})
		Expect(strings.Count(files[0].Content, "partial = true")).To(Equal(1), "only the table that is declared in part carries the attribute")
		evaluated := evaluate(files[0].Content)
		partial := evaluated.Tables[1]
		Expect(partial.Partial).To(BeTrue())
		Expect(evaluated.Tables[0].Partial).To(BeFalse())
		Expect(partial.Columns).To(HaveLen(2))
		Expect([]any{partial.Indexes, partial.ForeignKeys, partial.Checks}).To(Equal([]any{[]migrate.Index(nil), []migrate.ForeignKey(nil), []migrate.Check(nil)}))
		Expect(partial.Columns[0].Default).To(Equal(&migrate.Default{Name: "DF_orders_status", Expression: "(N'new')"}))
	})

	It("never writes the attribute for a full export, and the partial export it makes plans no change to the table it was read from", func() {
		Expect(export(exportedSchema(), exportOptions)[0].Content).NotTo(ContainSubstring("partial"))

		original := exportedSchema()
		declared := evaluate(export(original, native.HCLOptions{Columns: map[string][]string{"orders": {"status", "qty"}}})[0].Content)
		artifact := planBetween(original, declared)
		Expect(artifact.Changes).To(BeEmpty(), "everything the declaration leaves out is preserved, not dropped")
		Expect(artifact.Preserved).NotTo(BeEmpty())
		for _, c := range artifact.Preserved {
			Expect(c.Object).To(Equal(orders))
		}
	})

	DescribeTable("the evaluator refuses the attributes it reads when they contradict the declaration",
		func(column, index, message string) {
			var realm schema.Realm
			hcl := "schema \"dbo\" {}\ntable \"t\" {\n  schema = schema.dbo\n  column \"a\" {\n    type = int\n" + column + "  }\n" + index + "}\n"
			Expect(sqlserver.EvalHCLBytes([]byte(hcl), &realm, nil)).To(MatchError(ContainSubstring(message)))
		},
		Entry("a default name without a default", `    default_name = "DF_a"`+"\n", "", "default_name must be a non-empty string beside a default"),
		Entry("a unique constraint with a filter", "", "  index \"UQ\" {\n    unique = true\n    constraint = true\n    columns = [column.a]\n    where = \"a > 0\"\n  }\n",
			"a UNIQUE constraint must be unique, with no where or include"),
		Entry("a unique constraint that is not unique", "", "  index \"UQ\" {\n    constraint = true\n    columns = [column.a]\n  }\n",
			"a UNIQUE constraint must be unique, with no where or include"),
	)

	It("refuses a request for a column or table that is not there", func() {
		_, err := native.ExportHCL(exportedSchema(), native.HCLOptions{Columns: map[string][]string{"orders": {"nope"}}})
		Expect(err).To(MatchError(ContainSubstring(`table "orders" has no column "nope"`)))
		_, err = native.ExportHCL(exportedSchema(), native.HCLOptions{Columns: map[string][]string{"nope": {"id"}}})
		Expect(err).To(MatchError(ContainSubstring(`table "nope", which is not in the schema`)))
	})
})
