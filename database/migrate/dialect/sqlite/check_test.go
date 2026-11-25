package sqlite_test

import (
	"database/sql"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQLite edited checks", func() {
	var db *sql.DB
	BeforeEach(func() {
		db = openDB("")
		mustExec(db, "CREATE TABLE tasks (id integer NOT NULL, state text NOT NULL, CONSTRAINT state_known CHECK (state IN ('a', 'b')))", "INSERT INTO tasks VALUES (1, 'a')")
	})

	const base = `schema "main" {}
table "tasks" {
  schema = schema.main
  column "id" { type = integer }
  column "state" { type = text }
  check "state_known" { expr = "EXPR" }
}`

	It("plans an edited check as one change the declared scope keeps, rebuilding the table with its rows", func(ctx SpecContext) {
		plan := func(expr string) engine.Artifact {
			return mustPlan(ctx, db, declaredScope("tasks"), strings.Replace(base, "EXPR", expr, 1))
		}
		Expect(plan("state IN ('a', 'b')").Units).To(BeEmpty())

		edited := plan("state IN ('a', 'b', 'c')")
		Expect(changeIDs(edited)).To(Equal([]string{changeID("modify", "check", "tasks", "state_known")}))
		Expect(edited.Preserved).To(BeEmpty())
		Expect(mustApply(ctx, db, edited, engine.Acknowledgements(edited)...).Outcome).To(Equal(engine.Applied))

		_, err := db.Exec("INSERT INTO tasks VALUES (2, 'c')")
		Expect(err).NotTo(HaveOccurred())
		Expect(plan("state IN ('a', 'b', 'c')").Units).To(BeEmpty())
	})
})
