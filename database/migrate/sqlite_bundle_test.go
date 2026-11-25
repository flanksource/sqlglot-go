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

func changeIDOf(action, kind, table, member string) string {
	id, err := json.Marshal([]string{action, kind, "main", table, member})
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

	It("keeps an undeclared column, with its data and its index, through a rebuild, and WithDrops removes them", func(ctx context.Context) {
		exec(ctx,
			`ALTER TABLE items ADD COLUMN notes TEXT`,
			`CREATE INDEX items_notes ON items (notes)`,
			`INSERT INTO items (id, label, notes) VALUES ('i1', '7', 'dba note')`)

		Expect(Apply(ctx, dsn, bundleOf(retyped))).To(Succeed())
		Expect(columnNames(ctx, database, "items")).To(Equal([]string{"id", "label", "notes"}))
		var label, note string
		Expect(database.QueryRowContext(ctx, `SELECT typeof(label), notes FROM items WHERE id = 'i1'`).Scan(&label, &note)).To(Succeed())
		Expect([]string{label, note}).To(Equal([]string{"integer", "dba note"}))
		Expect(schemaObjects(ctx, database)).To(ContainElement(sqliteObject{"index", "items_notes", "items"}))

		Expect(Apply(ctx, dsn, bundleOf(retyped), WithDrops())).To(Succeed())
		Expect(columnNames(ctx, database, "items")).To(Equal([]string{"id", "label"}))
		Expect(schemaObjects(ctx, database)).NotTo(ContainElement(sqliteObject{"index", "items_notes", "items"}))
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

	It("previews a rebuild with the drops the declared scope preserves", func(ctx context.Context) {
		exec(ctx, `ALTER TABLE items ADD COLUMN notes TEXT`)

		preview, err := PreviewBundle(ctx, database, "sqlite", bundleOf(retyped))
		Expect(err).NotTo(HaveOccurred())
		artifact := preview.Artifact
		Expect(artifact.Scope.Mode).To(Equal(engine.ScopeDeclared))
		Expect(artifact.Changes).To(ConsistOf(HaveField("ID", changeIDOf("modify", "column", "items", "label"))))
		Expect(artifact.Preserved).To(ConsistOf(HaveField("ID", changeIDOf("drop", "column", "items", "notes"))))
		Expect(artifact.Units).To(HaveLen(1))
		Expect(engine.Validate(*artifact)).To(Succeed())

		complete, err := PreviewBundle(ctx, database, "sqlite", bundleOf(retyped), WithDrops())
		Expect(err).NotTo(HaveOccurred())
		Expect(complete.Artifact.Preserved).To(BeEmpty())
		Expect(engine.Acknowledgements(*complete.Artifact)).To(ContainElement(changeIDOf("drop", "column", "items", "notes")))
	})
})
