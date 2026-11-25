package migrate

import (
	"fmt"
	"io/fs"
	"strings"
	"testing/fstest"

	pgdialect "github.com/flanksource/sqlglot-go/database/migrate/dialect/postgres"
	sqlitedialect "github.com/flanksource/sqlglot-go/database/migrate/dialect/sqlite"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// bundleDialect says how a dialect's bundle becomes the schema it plans toward.
type bundleDialect struct {
	name, schema string
	desired      func(fs.FS) (migrate.Schema, error)
}

var bundleDialects = []bundleDialect{
	{name: "sqlserver", schema: "dbo", desired: func(files fs.FS) (migrate.Schema, error) {
		realm, err := sqlServerTables(files, resolveOptions(nil))
		if err != nil {
			return migrate.Schema{}, err
		}
		return planner.FromAtlas(realm)
	}},
	{name: "postgres", schema: "public", desired: func(files fs.FS) (migrate.Schema, error) {
		cfg := resolveOptions(nil)
		loaded, err := loadHCL(hclSource{FS: files, Dir: cfg.dir, Input: cfg.input})
		if err != nil {
			return migrate.Schema{}, err
		}
		step, err := postgresStep(loaded, cfg)
		return step.Desired, err
	}},
	{name: "sqlite", schema: "public", desired: func(files fs.FS) (migrate.Schema, error) {
		step, err := sqliteSchemaStep(files, resolveOptions(nil))
		return step.Desired, err
	}},
}

func (d bundleDialect) block(attrs string) string {
	return fmt.Sprintf("schema %q {%s}\n", d.schema, attrs)
}

func (d bundleDialect) table(name string) string {
	return fmt.Sprintf("table %q {\n  schema = schema.%s\n  column \"id\" {\n    type = int\n    null = false\n  }\n}\n", name, d.schema)
}

// empty is a database holding nothing, as the dialect reads one: the Atlas
// dialects need the snapshot their inspection attaches.
func (d bundleDialect) empty(schemaName string) migrate.Schema {
	GinkgoHelper()
	if d.name == "sqlserver" {
		return migrate.Schema{}
	}
	hcl := fmt.Sprintf("schema %q {}", schemaName)
	if d.name == "postgres" {
		current, err := pgdialect.SchemaFromHCL(hcl)
		Expect(err).NotTo(HaveOccurred())
		return current
	}
	current, err := sqlitedialect.SchemaFromHCL(hcl)
	Expect(err).NotTo(HaveOccurred())
	return current
}

func (d bundleDialect) plan(desired migrate.Schema) engine.Artifact {
	GinkgoHelper()
	name := desired.Tables[0].Name.Schema
	artifact, err := engine.Plan(engine.PlanRequest{
		Dialect: d.name, Scope: engine.Scope{Mode: engine.ScopeComplete, Schemas: []string{name}}, Current: d.empty(name), Desired: desired,
	})
	Expect(err).NotTo(HaveOccurred())
	return artifact
}

var _ = Describe("a bundle whose HCL files each declare the schema block", func() {
	tables := []string{"alpha", "beta", "gamma"}

	DescribeTable("plans the same as the single-file equivalent",
		func(d bundleDialect) {
			perFile, single := fstest.MapFS{}, strings.Builder{}
			single.WriteString(d.block(""))
			for _, t := range tables {
				perFile[t+".hcl"] = &fstest.MapFile{Data: []byte(d.block("") + d.table(t))}
				single.WriteString(d.table(t))
			}
			got, err := d.desired(perFile)
			Expect(err).NotTo(HaveOccurred())
			want, err := d.desired(fstest.MapFS{"schema.hcl": &fstest.MapFile{Data: []byte(single.String())}})
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want))
			Expect(got.Tables).To(HaveLen(len(tables)))
			Expect(d.plan(got).Hash).To(Equal(d.plan(want).Hash))
		},
		Entry("SQL Server", bundleDialects[0]),
		Entry("PostgreSQL", bundleDialects[1]),
		Entry("SQLite", bundleDialects[2]),
	)

	DescribeTable("refuses two blocks of one name that differ, naming both files",
		func(d bundleDialect) {
			files := fstest.MapFS{
				"a.hcl": &fstest.MapFile{Data: []byte(d.block("") + d.table("alpha"))},
				"b.hcl": &fstest.MapFile{Data: []byte(d.block(` comment = "other" `) + d.table("beta"))},
			}
			_, err := d.desired(files)
			Expect(err).To(MatchError(SatisfyAll(
				ContainSubstring(fmt.Sprintf("schema %q is declared differently", d.schema)),
				ContainSubstring("a.hcl:1"), ContainSubstring("b.hcl:1"))))
		},
		Entry("SQL Server", bundleDialects[0]),
		Entry("PostgreSQL", bundleDialects[1]),
		Entry("SQLite", bundleDialects[2]),
	)

	partialTable := func(d bundleDialect, name, value string) string {
		return strings.Replace(d.table(name), "  schema = schema."+d.schema+"\n", "  schema = schema."+d.schema+"\n  partial = "+value+"\n", 1)
	}

	DescribeTable("reads a table's partial flag from its file, whatever the evaluator ignores",
		func(d bundleDialect) {
			files := fstest.MapFS{
				"a.hcl": &fstest.MapFile{Data: []byte(d.block("") + partialTable(d, "alpha", "true"))},
				"b.hcl": &fstest.MapFile{Data: []byte(d.block("") + d.table("beta"))},
			}
			desired, err := d.desired(files)
			Expect(err).NotTo(HaveOccurred())
			Expect(desired.Tables).To(HaveLen(2))
			Expect([]bool{desired.Tables[0].Partial, desired.Tables[1].Partial}).To(Equal([]bool{true, false}))
			Expect(desired.Tables[0].Name.Name).To(Equal("alpha"))
		},
		Entry("SQL Server", bundleDialects[0]),
		Entry("PostgreSQL", bundleDialects[1]),
		Entry("SQLite", bundleDialects[2]),
	)

	DescribeTable("refuses a partial value that is not a boolean, naming the file and the table",
		func(d bundleDialect) {
			files := fstest.MapFS{
				"a.hcl": &fstest.MapFile{Data: []byte(d.block("") + d.table("alpha"))},
				"b.hcl": &fstest.MapFile{Data: []byte(d.block("") + partialTable(d, "beta", `"yes"`))},
			}
			_, err := d.desired(files)
			Expect(err).To(MatchError(ContainSubstring(`b.hcl:4: table "beta": partial must be true or false`)))
		},
		Entry("SQL Server", bundleDialects[0]),
		Entry("PostgreSQL", bundleDialects[1]),
		Entry("SQLite", bundleDialects[2]),
	)

	DescribeTable("refuses the same table declared in two files, naming both",
		func(d bundleDialect) {
			files := fstest.MapFS{
				"a.hcl": &fstest.MapFile{Data: []byte(d.block("") + d.table("alpha"))},
				"b.hcl": &fstest.MapFile{Data: []byte(d.block("") + partialTable(d, "alpha", "true"))},
			}
			_, err := d.desired(files)
			Expect(err).To(MatchError(SatisfyAll(
				ContainSubstring(fmt.Sprintf("table %q is declared twice", d.schema+".alpha")),
				ContainSubstring("a.hcl:2"), ContainSubstring("b.hcl:2"))))
		},
		Entry("SQL Server", bundleDialects[0]),
		Entry("PostgreSQL", bundleDialects[1]),
		Entry("SQLite", bundleDialects[2]),
	)

	DescribeTable("still refuses a bundle that really declares two schemas",
		func(d bundleDialect, message string) {
			files := fstest.MapFS{
				"a.hcl": &fstest.MapFile{Data: []byte(d.block("") + d.table("alpha"))},
				"b.hcl": &fstest.MapFile{Data: []byte(`schema "other" {}` + "\n" + strings.ReplaceAll(d.table("beta"), "schema."+d.schema, "schema.other"))},
			}
			_, err := d.desired(files)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("SQL Server", bundleDialects[0], `must declare exactly schema "dbo", got ["dbo", "other"]`),
		Entry("PostgreSQL", bundleDialects[1], `schema-scoped bundles must declare only "public"`),
		Entry("SQLite", bundleDialects[2], "expected one schema, got 2"),
	)

	It("evaluates an exported one-file-per-table SQL Server bundle, foreign keys across files included", func() {
		parent := migrate.Name{Schema: "dbo", Name: "customers"}
		original := migrate.Schema{Tables: []migrate.Table{
			{Name: parent, Columns: []migrate.Column{{Name: "id", Type: "int"}},
				Indexes: []migrate.Index{{Name: "PK_customers", Primary: true, Unique: true, Columns: []migrate.IndexColumn{{Name: "id"}}}}},
			{Name: migrate.Name{Schema: "dbo", Name: "orders"}, Columns: []migrate.Column{{Name: "id", Type: "int"}, {Name: "customer_id", Type: "int"}},
				ForeignKeys: []migrate.ForeignKey{{Name: "FK_orders_customers", Columns: []string{"customer_id"}, References: parent,
					ReferencedColumns: []string{"id"}, OnDelete: "CASCADE", OnUpdate: "NO ACTION"}}},
		}}
		exported, err := planner.ExportHCL(original, planner.HCLOptions{Layout: planner.HCLFilePerTable})
		Expect(err).NotTo(HaveOccurred())
		files := fstest.MapFS{}
		for _, f := range exported {
			files[f.Name] = &fstest.MapFile{Data: []byte(f.Content)}
		}
		desired, err := bundleDialects[0].desired(files)
		Expect(err).NotTo(HaveOccurred())
		Expect(desired).To(Equal(original))
	})
})
