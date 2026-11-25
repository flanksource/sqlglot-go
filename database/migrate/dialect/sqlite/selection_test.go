package sqlite_test

import (
	"database/sql"

	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const selectionHCL = `schema "main" {}
table "items" {
  schema = schema.main
  column "id" { type = integer }
  column "label" { type = text }
  column "note" {
    type = text
    null = true
  }
  index "idx_items_label" { columns = [column.label] }
}
table "tags" {
  schema = schema.main
  column "id" { type = integer }
}`

var _ = Describe("SQLite selective plans", func() {
	var (
		db          *sql.DB
		addTags     = changeID("add", "table", "tags", "")
		addNote     = changeID("add", "column", "items", "note")
		addLabelIdx = changeID("add", "index", "items", "idx_items_label")
	)
	BeforeEach(func() {
		db = openDB("")
		mustExec(db, "CREATE TABLE items (id integer NOT NULL, label text NOT NULL)")
	})

	It("plans only the selected changes and leaves the other candidates for a later plan", func(ctx SpecContext) {
		a := mustPlan(ctx, db, completeScope(), selectionHCL, addTags)
		Expect(changeIDs(a)).To(Equal([]string{addTags}))
		Expect(a.Selection).To(Equal([]string{addTags}))
		Expect(statements(a)).To(ConsistOf(ContainSubstring("CREATE TABLE `tags`")))
	})

	It("applies a partial plan, recognizes it as applied while unselected changes remain, and plans the rest", func(ctx SpecContext) {
		a := mustPlan(ctx, db, completeScope(), selectionHCL, addNote, addTags)
		Expect(mustApply(ctx, db, a).Outcome).To(Equal(engine.Applied))
		Expect(mustApply(ctx, db, a).Outcome).To(Equal(engine.AlreadyApplied))

		rest := mustPlan(ctx, db, completeScope(), selectionHCL)
		Expect(changeIDs(rest)).To(Equal([]string{addLabelIdx}))
	})
})
