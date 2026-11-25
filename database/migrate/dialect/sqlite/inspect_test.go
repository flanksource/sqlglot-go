package sqlite_test

import (
	"database/sql"

	"github.com/flanksource/sqlglot-go/database/migrate/dialect/sqlite"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func tableNames(s migrate.Schema) []string {
	names := []string{}
	for _, t := range s.Tables {
		names = append(names, t.Name.Name)
	}
	return names
}

var _ = Describe("SQLite inspection", func() {
	var db *sql.DB
	BeforeEach(func() {
		db = openDB("")
		mustExec(db, "CREATE TABLE items (id integer NOT NULL, label text)", "CREATE TABLE audited (id integer NOT NULL)", "CREATE TABLE other (id integer NOT NULL)")
	})

	It("needs exactly one schema and names what it was given", func(ctx SpecContext) {
		for _, schemas := range [][]string{nil, {"main", "temp"}, {"other"}} {
			_, err := sqlite.Dialect.Inspect(ctx, db, engine.Scope{Mode: engine.ScopeComplete, Schemas: schemas})
			Expect(err).To(HaveOccurred())
		}
		_, err := sqlite.Dialect.Inspect(ctx, db, engine.Scope{Mode: engine.ScopeComplete, Schemas: []string{"main", "temp"}})
		Expect(err).To(MatchError(ContainSubstring("[main temp]")))
	})

	It("returns exactly the declared objects that exist, and the whole schema when complete", func(ctx SpecContext) {
		scope := declaredScope("items", "missing")
		declared, err := sqlite.Dialect.Inspect(ctx, db, scope)
		Expect(err).NotTo(HaveOccurred())
		Expect(tableNames(declared.Schema)).To(Equal([]string{"items"}))

		complete, err := sqlite.Dialect.Inspect(ctx, db, completeScope())
		Expect(err).NotTo(HaveOccurred())
		Expect(tableNames(complete.Schema)).To(Equal([]string{"audited", "items", "other"}))
	})

	It("refuses only a rebuild that would discard a trigger, never a table with one, a change in place or the schema", func(ctx SpecContext) {
		mustExec(db,
			"CREATE TRIGGER audited_log AFTER INSERT ON audited BEGIN SELECT 1; END",
			"CREATE VIEW item_labels AS SELECT label FROM items")
		complete, err := sqlite.Dialect.Inspect(ctx, db, completeScope())
		Expect(err).NotTo(HaveOccurred())
		Expect(complete.Refusals).To(BeEmpty())
		Expect(tableNames(complete.Schema)).To(Equal([]string{"audited", "items", "other"}))
		Expect(complete.Schema.Definitions).To(ConsistOf(HaveField("Name.Name", "item_labels")))

		unrelated := `schema "main" {}
table "other" {
  schema = schema.main
  column "id" { type = integer }
  column "extra" {
    type = text
    null = true
  }
}
table "items" {
  schema = schema.main
  column "id" { type = integer }
  column "label" {
    type = text
    null = true
  }
}`
		a, err := planLive(ctx, db, declaredScope("other", "items"), desiredOf(unrelated))
		Expect(err).NotTo(HaveOccurred())
		Expect(changeIDs(a)).To(Equal([]string{changeID("add", "column", "other", "extra")}))
		Expect(mustApply(ctx, db, a).Outcome).To(Equal(engine.Applied))

		inPlace := `schema "main" {}
table "audited" {
  schema = schema.main
  column "id" { type = integer }
  column "extra" {
    type = text
    null = true
  }
}`
		a, err = planLive(ctx, db, declaredScope("audited"), desiredOf(inPlace))
		Expect(err).NotTo(HaveOccurred())
		Expect(changeIDs(a)).To(Equal([]string{changeID("add", "column", "audited", "extra")}))

		rebuilt := `schema "main" {}
table "audited" {
  schema = schema.main
  column "id" { type = text }
}`
		_, err = planLive(ctx, db, declaredScope("audited"), desiredOf(rebuilt))
		Expect(err).To(MatchError(ContainSubstring("trigger \"audited_log\" would be lost when the table is rebuilt")))
	})
})
