package sqlite_test

import (
	"database/sql"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQLite declarations that are complete or partial", func() {
	var db *sql.DB
	BeforeEach(func() {
		db = openDB("")
		mustExec(db,
			"CREATE TABLE items (id integer NOT NULL, label text NOT NULL, extra text, CONSTRAINT items_label_ck CHECK (length(label) > 0), CONSTRAINT items_extra_ck CHECK (extra <> 'x'))",
			"CREATE INDEX items_extra_idx ON items (extra)",
			"INSERT INTO items VALUES (1, 'kept', 'dba value')")
	})

	// declared is the items declaration without its extra column, index and check.
	declared := func(partial bool) string {
		hcl := `schema "main" {}
table "items" {
  schema = schema.main
  column "id" { type = integer }
  column "label" { type = text }
  check "items_label_ck" { expr = "length(label) > 0" }
}`
		if partial {
			return strings.Replace(hcl, "  schema = schema.main\n", "  schema = schema.main\n  partial = true\n", 1)
		}
		return hcl
	}
	drops := []string{
		changeID("drop", "column", "items", "extra"),
		changeID("drop", "index", "items", "items_extra_idx"),
		changeID("drop", "check", "items", "items_extra_ck"),
	}

	It("drops the undeclared column, index and check of a complete declaration, each acknowledged, keeping the rows", func(ctx SpecContext) {
		a := mustPlan(ctx, db, declaredScope("items"), declared(false))
		Expect(a.Preserved).To(BeEmpty())
		Expect(changeIDs(a)).To(ConsistOf(drops))
		Expect(engine.Acknowledgements(a)).To(ConsistOf(drops))
		Expect(statements(a)).To(ContainElements(
			"CREATE TABLE `new_items` (`id` integer NOT NULL, `label` text NOT NULL, CONSTRAINT `items_label_ck` CHECK (length(label) > 0))",
			"DROP TABLE `items`"))

		mustApply(ctx, db, a, drops...)
		Expect(columnNames(db, "items")).To(Equal([]string{"id", "label"}))
		var label string
		Expect(db.QueryRow("SELECT label FROM items WHERE id = 1").Scan(&label)).To(Succeed())
		Expect(label).To(Equal("kept"))
		Expect(mustPlan(ctx, db, declaredScope("items"), declared(false)).Changes).To(BeEmpty())
	})

	It("plans nothing for a partial declaration and lists the three drops as preserved, in either scope", func(ctx SpecContext) {
		for _, scope := range []engine.Scope{declaredScope("items"), completeScope()} {
			a := mustPlan(ctx, db, scope, declared(true))
			Expect(a.Changes).To(BeEmpty())
			Expect(a.Units).To(BeEmpty())
			Expect(changeIDs(engine.Artifact{Changes: a.Preserved})).To(ConsistOf(drops))
			Expect(engine.Validate(a)).To(Succeed())
		}
	})

	It("rebuilds a partial table for a changed column and keeps the undeclared column, index and check through it", func(ctx SpecContext) {
		retyped := strings.Replace(declared(true), `column "label" { type = text }`, "column \"label\" {\n    type    = text\n    default = \"n/a\"\n  }", 1)
		a := mustPlan(ctx, db, declaredScope("items"), retyped)
		Expect(changeIDs(a)).To(Equal([]string{changeID("modify", "column", "items", "label")}))
		Expect(changeIDs(engine.Artifact{Changes: a.Preserved})).To(ConsistOf(drops))

		mustApply(ctx, db, a, engine.Acknowledgements(a)...)
		Expect(columnNames(db, "items")).To(Equal([]string{"id", "label", "extra"}))
		var indexed, extra string
		Expect(db.QueryRow(`SELECT name FROM sqlite_schema WHERE type = 'index' AND name = 'items_extra_idx'`).Scan(&indexed)).To(Succeed())
		Expect(db.QueryRow(`SELECT extra FROM items WHERE id = 1`).Scan(&extra)).To(Succeed())
		Expect(extra).To(Equal("dba value"))
		Expect(tableSQL(db, "items")).To(ContainSubstring("items_extra_ck"))
	})

	It("drops only the index when that is all a complete declaration omits", func(ctx SpecContext) {
		mustExec(db, "DROP INDEX items_extra_idx", "CREATE INDEX items_label_idx ON items (label)")
		hcl := `schema "main" {}
table "items" {
  schema = schema.main
  column "id" { type = integer }
  column "label" { type = text }
  column "extra" {
    type = text
    null = true
  }
  check "items_label_ck" { expr = "length(label) > 0" }
  check "items_extra_ck" { expr = "extra <> 'x'" }
}`
		a := mustPlan(ctx, db, declaredScope("items"), hcl)
		drop := changeID("drop", "index", "items", "items_label_idx")
		Expect(changeIDs(a)).To(Equal([]string{drop}))
		Expect(statements(a)).To(Equal([]string{"DROP INDEX `items_label_idx`"}))
		mustApply(ctx, db, a, drop)
		Expect(mustPlan(ctx, db, declaredScope("items"), hcl).Changes).To(BeEmpty())
	})
})
