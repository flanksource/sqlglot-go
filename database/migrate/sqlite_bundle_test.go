package migrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing/fstest"

	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/zclconf/go-cty/cty"
)

const (
	itemsTable = `table "items" {
  schema = schema.public
  column "id" { type = text }
  column "label" { type = text }
  primary_key { columns = [column.id] }
}
`
	scratchTable = `table "scratch" {
  schema = schema.public
  column "id" { type = text }
  column "declared" {
    type = text
    null = true
  }
}
`
)

func columnChangeID(action, member string) string {
	id, err := json.Marshal([]string{action, "column", "main", "items", member})
	Expect(err).NotTo(HaveOccurred())
	return string(id)
}

func bundleOf(hcl string) fstest.MapFS {
	return fstest.MapFS{"schema.hcl": &fstest.MapFile{Data: []byte(`schema "public" {}` + "\n" + hcl)}}
}

var _ = Describe("SQLite bundle planned through the engine", func() {
	var (
		dsn      string
		database *sql.DB
	)
	BeforeEach(func(ctx context.Context) {
		dsn = filepath.Join(GinkgoT().TempDir(), "engine.db")
		Expect(Apply(ctx, dsn, bundleOf(itemsTable))).To(Succeed())
		database = openCharacterizedSQLite(ctx, dsn)
	})

	exec := func(ctx context.Context, statements ...string) {
		for _, statement := range statements {
			_, err := database.ExecContext(ctx, statement)
			Expect(err).NotTo(HaveOccurred(), statement)
		}
	}
	retyped := strings.Replace(itemsTable, `column "label" { type = text }`, `column "label" { type = integer }`, 1)

	partial := func(hcl string) string {
		return strings.Replace(hcl, "  schema = schema.public\n", "  schema = schema.public\n  partial = true\n", 1)
	}
	withNotes := func(ctx context.Context) {
		exec(ctx,
			`ALTER TABLE items ADD COLUMN notes TEXT`,
			`CREATE INDEX items_notes ON items (notes)`,
			`INSERT INTO items (id, label, notes) VALUES ('i1', '7', 'dba note')`)
	}

	It("keeps an undeclared column with its data through a rebuild without WithDrops, drops its index, names it in a warning, and WithDrops removes it", func(ctx context.Context) {
		withNotes(ctx)

		preview, err := PreviewBundle(ctx, database, "sqlite", bundleOf(retyped))
		Expect(err).NotTo(HaveOccurred())
		Expect(preview.Warnings).To(ConsistOf(`column "notes" of table main.items is in the database but not declared, and was not dropped: declare the column, mark the table ` + "`partial = true`" + `, or apply with WithDrops`))

		Expect(Apply(ctx, dsn, bundleOf(retyped))).To(Succeed())
		Expect(columnNames(ctx, database, "items")).To(Equal([]string{"id", "label", "notes"}))
		var label, note string
		Expect(database.QueryRowContext(ctx, `SELECT typeof(label), notes FROM items WHERE id = 'i1'`).Scan(&label, &note)).To(Succeed())
		Expect([]string{label, note}).To(Equal([]string{"integer", "dba note"}))
		Expect(schemaObjects(ctx, database)).NotTo(ContainElement(sqliteObject{"index", "items_notes", "items"}))

		again, err := PreviewBundle(ctx, database, "sqlite", bundleOf(retyped))
		Expect(err).NotTo(HaveOccurred())
		Expect(again.Artifact.Units).To(BeEmpty(), "nothing is left to run, and the warning is reported again")
		Expect(engine.Validate(*again.Artifact)).To(Succeed())
		Expect(again.Warnings).To(HaveLen(1))

		Expect(Apply(ctx, dsn, bundleOf(retyped), WithDrops())).To(Succeed())
		Expect(columnNames(ctx, database, "items")).To(Equal([]string{"id", "label"}))
	})

	It("keeps an undeclared column and its index through a rebuild of a partial table, with nothing to report", func(ctx context.Context) {
		withNotes(ctx)

		preview, err := PreviewBundle(ctx, database, "sqlite", bundleOf(partial(retyped)))
		Expect(err).NotTo(HaveOccurred())
		Expect(preview.Warnings).To(BeEmpty())

		Expect(Apply(ctx, dsn, bundleOf(partial(retyped)))).To(Succeed())
		Expect(columnNames(ctx, database, "items")).To(Equal([]string{"id", "label", "notes"}))
		Expect(schemaObjects(ctx, database)).To(ContainElement(sqliteObject{"index", "items_notes", "items"}))

		Expect(Apply(ctx, dsn, bundleOf(partial(retyped)), WithDrops())).To(Succeed())
		Expect(columnNames(ctx, database, "items")).To(Equal([]string{"id", "label", "notes"}), "WithDrops never drops what a partial table does not name")
	})

	It("drops an undeclared index of a complete declaration without WithDrops", func(ctx context.Context) {
		exec(ctx, `CREATE INDEX items_label ON items (label)`)
		Expect(Apply(ctx, dsn, bundleOf(itemsTable))).To(Succeed())
		Expect(schemaObjects(ctx, database)).NotTo(ContainElement(sqliteObject{"index", "items_label", "items"}))
	})

	It("hides an excluded table from the live schema and the declared one, even under WithDrops", func(ctx context.Context) {
		exec(ctx,
			`CREATE TABLE scratch (junk TEXT)`, `INSERT INTO scratch VALUES ('dba data')`,
			`CREATE TABLE ledger (entry TEXT)`, `CREATE TABLE other (value TEXT)`)

		Expect(Apply(ctx, dsn, bundleOf(itemsTable+scratchTable), WithExclude("scratch", "ledger"), WithDrops())).To(Succeed())

		Expect(columnNames(ctx, database, "scratch")).To(Equal([]string{"junk"}))
		var tables []string
		rows, err := database.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type = 'table' ORDER BY name`)
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(rows.Close()).To(Succeed()) }()
		for rows.Next() {
			var name string
			Expect(rows.Scan(&name)).To(Succeed())
			tables = append(tables, name)
		}
		Expect(tables).To(Equal([]string{"items", "ledger", "scratch"}), "other was dropped; the excluded tables were not touched")
	})

	It("refuses an exclusion of a table member, which a rebuild would lose with its data", func(ctx context.Context) {
		Expect(Apply(ctx, dsn, bundleOf(itemsTable), WithExclude("items.label"))).
			To(MatchError(ContainSubstring(`exclusion "items.label" names a member of a table`)))
	})

	It("evaluates HCL variables before planning", func(ctx context.Context) {
		const hcl = `variable "state" { type = string }
schema "public" {}
table "docs" {
  schema = schema.public
  column "id" { type = text }
  column "state" {
    type = text
    default = var.state
  }
}
`
		files := fstest.MapFS{"schema.hcl": &fstest.MapFile{Data: []byte(hcl)}}
		Expect(Apply(ctx, dsn, files, WithVariables(map[string]cty.Value{"state": cty.StringVal("draft")}))).To(Succeed())
		exec(ctx, `INSERT INTO docs (id) VALUES ('d1')`)
		var state string
		Expect(database.QueryRowContext(ctx, `SELECT state FROM docs WHERE id = 'd1'`).Scan(&state)).To(Succeed())
		Expect(state).To(Equal("draft"))

		Expect(Apply(ctx, dsn, files, WithVariables(map[string]cty.Value{"state": cty.StringVal("draft")}))).To(Succeed())
		Expect(Apply(ctx, dsn, files)).To(MatchError(ContainSubstring("state")), "an unset variable is an error, not an empty default")
	})

	It("previews a rebuild without the column drop an unreviewed apply leaves out, and the reviewed plan with it", func(ctx context.Context) {
		exec(ctx, `ALTER TABLE items ADD COLUMN notes TEXT`)

		preview, err := PreviewBundle(ctx, database, "sqlite", bundleOf(retyped))
		Expect(err).NotTo(HaveOccurred())
		artifact := preview.Artifact
		Expect(artifact.Scope.Mode).To(Equal(engine.ScopeDeclared))
		Expect(artifact.Changes).To(ConsistOf(HaveField("ID", columnChangeID("modify", "label"))))
		Expect(artifact.Units).To(HaveLen(1))
		Expect(engine.Validate(*artifact)).To(Succeed())
		Expect(preview.Warnings).To(HaveLen(1))

		reviewed, err := PreviewBundle(ctx, database, "sqlite", bundleOf(retyped), WithReviewedPlan())
		Expect(err).NotTo(HaveOccurred())
		Expect(reviewed.Warnings).To(BeEmpty())
		Expect(reviewed.Artifact.Preserved).To(BeEmpty())
		Expect(engine.Acknowledgements(*reviewed.Artifact)).To(ContainElement(columnChangeID("drop", "notes")))
		Expect(engine.Validate(*reviewed.Artifact)).To(Succeed())

		held, err := PreviewBundle(ctx, database, "sqlite", bundleOf(partial(retyped)), WithReviewedPlan())
		Expect(err).NotTo(HaveOccurred())
		Expect(held.Artifact.Preserved).To(ConsistOf(HaveField("ID", columnChangeID("drop", "notes"))))

		complete, err := PreviewBundle(ctx, database, "sqlite", bundleOf(retyped), WithDrops())
		Expect(err).NotTo(HaveOccurred())
		Expect(complete.Warnings).To(BeEmpty())
		Expect(engine.Acknowledgements(*complete.Artifact)).To(ContainElement(columnChangeID("drop", "notes")))
	})
})
