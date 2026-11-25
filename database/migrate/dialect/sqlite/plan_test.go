package sqlite_test

import (
	"database/sql"
	"errors"
	"time"

	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const itemsHCL = `schema "main" {}
table "items" {
  schema = schema.main
  column "id" { type = integer }
  column "label" { type = text }
}`

var _ = Describe("SQLite plans through the engine", func() {
	var db *sql.DB
	BeforeEach(func() { db = openDB("") })

	It("plans a create, applies it, re-plans to nothing and refuses an edited statement", func(ctx SpecContext) {
		a := mustPlan(ctx, db, completeScope(), itemsHCL)
		Expect(changeIDs(a)).To(Equal([]string{changeID("add", "table", "items", "")}))
		Expect(statements(a)).To(Equal([]string{"CREATE TABLE `items` (`id` integer NOT NULL, `label` text NOT NULL)"}))
		Expect(engine.Acknowledgements(a)).To(BeEmpty())
		Expect(engine.Validate(a)).To(Succeed())

		edited := a
		edited.Units = append([]engine.Unit(nil), a.Units...)
		edited.Units[0].Ops = append([]engine.Op(nil), a.Units[0].Ops...)
		edited.Units[0].Ops[0].Statements = []engine.Statement{{SQL: "DROP TABLE `items`"}}
		Expect(engine.Validate(edited)).To(MatchError(ContainSubstring("hash mismatch")))

		result := mustApply(ctx, db, a)
		Expect(result.Outcome).To(Equal(engine.Applied))
		Expect(result.Units).To(HaveLen(1))
		Expect(result.Units[0].Statements).To(Equal(1))

		again := mustPlan(ctx, db, completeScope(), itemsHCL)
		Expect(again.Changes).To(BeEmpty())
		Expect(again.Units).To(BeEmpty())
		Expect(again.Baseline).To(Equal(a.Desired))
		Expect(mustApply(ctx, db, a).Outcome).To(Equal(engine.AlreadyApplied))
	})

	It("marks a column drop destructive, needs its acknowledgement and keeps the rows through the rebuild", func(ctx SpecContext) {
		mustExec(db, "CREATE TABLE items (id integer NOT NULL, label text NOT NULL, omitted text)", "INSERT INTO items VALUES (1, 'kept', 'gone')")
		a := mustPlan(ctx, db, completeScope(), itemsHCL)
		drop := changeID("drop", "column", "items", "omitted")
		Expect(changeIDs(a)).To(Equal([]string{drop}))
		Expect(statements(a)).To(Equal([]string{
			"CREATE TABLE `new_items` (`id` integer NOT NULL, `label` text NOT NULL)",
			"INSERT INTO `new_items` (`id`, `label`) SELECT `id`, `label` FROM `items`",
			"DROP TABLE `items`",
			"ALTER TABLE `new_items` RENAME TO `items`",
		}))
		Expect(engine.Acknowledgements(a)).To(Equal([]string{drop}))

		_, err := apply(ctx, db, a)
		Expect(err).To(MatchError(ContainSubstring("requires acknowledgements")))
		_, err = apply(ctx, db, a, drop, "unknown")
		Expect(err).To(MatchError(ContainSubstring("unknown or duplicate destructive acknowledgement")))
		Expect(mustApply(ctx, db, a, drop).Outcome).To(Equal(engine.Applied))

		var label string
		Expect(db.QueryRow("SELECT label FROM items WHERE id = 1").Scan(&label)).To(Succeed())
		Expect(label).To(Equal("kept"))
		Expect(columnNames(db, "items")).To(Equal([]string{"id", "label"}))
		Expect(mustPlan(ctx, db, completeScope(), itemsHCL).Changes).To(BeEmpty())
	})

	It("keeps an undeclared column and table untouched in declared scope", func(ctx SpecContext) {
		mustExec(db,
			"CREATE TABLE items (id integer NOT NULL, label text NOT NULL, omitted text)", "INSERT INTO items VALUES (1, 'kept', 'dba value')",
			"CREATE TABLE scratch (value text)", "INSERT INTO scratch VALUES ('dba table')")
		a := mustPlan(ctx, db, declaredScope("items"), itemsHCL)
		Expect(a.Changes).To(BeEmpty())
		Expect(a.Preserved).To(HaveLen(1))
		Expect(mustApply(ctx, db, a).Outcome).To(Equal(engine.AlreadyApplied))
		var omitted, scratch string
		Expect(db.QueryRow("SELECT omitted FROM items").Scan(&omitted)).To(Succeed())
		Expect(db.QueryRow("SELECT value FROM scratch").Scan(&scratch)).To(Succeed())
		Expect([]string{omitted, scratch}).To(Equal([]string{"dba value", "dba table"}))
	})

	It("refuses an artifact once a declared table changed after the preview and changes nothing", func(ctx SpecContext) {
		mustExec(db, "CREATE TABLE items (id integer NOT NULL)")
		a := mustPlan(ctx, db, declaredScope("items"), itemsHCL)
		mustExec(db, "ALTER TABLE items ADD COLUMN intervening text")
		_, err := apply(ctx, db, a)
		var drift *engine.DriftError
		Expect(errors.As(err, &drift)).To(BeTrue(), "got %v", err)
		Expect(columnNames(db, "items")).To(Equal([]string{"id", "intervening"}))
	})

	It("waits at most the lock timeout for another writer, then applies once it is gone", func(ctx SpecContext) {
		a := mustPlan(ctx, db, completeScope(), itemsHCL)
		writer, err := db.Conn(ctx)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(writer.Close)
		_, err = writer.ExecContext(ctx, "BEGIN IMMEDIATE")
		Expect(err).NotTo(HaveOccurred())

		started := time.Now()
		_, err = engine.Apply(ctx, db, engine.ApplyRequest{Artifact: a, Hash: a.Hash, LockTimeout: 100 * time.Millisecond})
		Expect(err).To(MatchError(ContainSubstring("acquire SQLite migration write lock")))
		Expect(time.Since(started)).To(BeNumerically("<", 5*time.Second))

		_, err = writer.ExecContext(ctx, "ROLLBACK")
		Expect(err).NotTo(HaveOccurred())
		Expect(mustApply(ctx, db, a).Outcome).To(Equal(engine.Applied))
	})

	It("gives every selected change an op when one rebuild implements several, and refuses a partial selection", func(ctx SpecContext) {
		mustExec(db, "CREATE TABLE items (id integer NOT NULL, label text NOT NULL, omitted text)")
		desired := `schema "main" {}
table "items" {
  schema = schema.main
  column "id" { type = integer }
  column "label" { type = text }
  index "idx_items_label" { columns = [column.label] }
}`
		drop, index := changeID("drop", "column", "items", "omitted"), changeID("add", "index", "items", "idx_items_label")
		_, err := planLive(ctx, db, completeScope(), desiredOf(desired), drop)
		Expect(err).To(MatchError(ContainSubstring("requires unselected changes " + index)))

		a := mustPlan(ctx, db, completeScope(), desired, drop, index)
		withOps := map[string]bool{}
		for _, u := range a.Units {
			for _, op := range u.Ops {
				withOps[op.Change] = true
			}
		}
		Expect(withOps).To(Equal(map[string]bool{drop: true, index: true}))
		Expect(a.Units).To(HaveLen(1), "the changes of one table rebuild form one unit")

		indexOnly := mustPlan(ctx, db, completeScope(), desired, index)
		Expect(statements(indexOnly)).To(ConsistOf(HavePrefix("CREATE INDEX")))
	})

	It("blocks a NULL to NOT NULL change and a new unique index on existing data until the data is clean", func(ctx SpecContext) {
		mustExec(db, "CREATE TABLE items (id integer NOT NULL, label text)", "INSERT INTO items VALUES (1, NULL), (2, 'dup'), (3, 'dup')")
		notNull := `schema "main" {}
table "items" {
  schema = schema.main
  column "id" { type = integer }
  column "label" { type = text }
}`
		a := mustPlan(ctx, db, completeScope(), notNull)
		var pre *engine.PreconditionError
		_, err := apply(ctx, db, a, changeID("modify", "column", "items", "label"))
		Expect(errors.As(err, &pre)).To(BeTrue(), "got %v", err)
		Expect(pre.Message).To(ContainSubstring("NULL"))

		mustExec(db, "UPDATE items SET label = 'one' WHERE id = 1")
		unique := `schema "main" {}
table "items" {
  schema = schema.main
  column "id" { type = integer }
  column "label" { type = text }
  index "items_label_key" {
    unique = true
    columns = [column.label]
  }
}`
		a = mustPlan(ctx, db, completeScope(), unique)
		Expect(a.Units).NotTo(BeEmpty())
		_, err = apply(ctx, db, a, engine.Acknowledgements(a)...)
		Expect(errors.As(err, &pre)).To(BeTrue(), "got %v", err)
		Expect(pre.Message).To(ContainSubstring("duplicate"))
	})
})
