//go:build integration

package postgres_test

import (
	"database/sql"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PostgreSQL declarations that are complete or partial", func() {
	var db *sql.DB
	BeforeEach(func(ctx SpecContext) {
		db = openDB(ctx)
		mustExec(db,
			"CREATE TABLE tasks (id integer NOT NULL, label text NOT NULL, extra text)",
			"CREATE INDEX tasks_label_idx ON tasks (label)",
			"ALTER TABLE tasks ADD CONSTRAINT tasks_label_ck CHECK (length(label) > 0)",
			"INSERT INTO tasks VALUES (1, 'kept', 'dba value')")
	})

	declared := func(partial bool, label string) string {
		hcl := `schema "public" {}
table "tasks" {
  schema = schema.public
  column "id" { type = integer }
  column "label" { type = ` + label + ` }
}`
		if partial {
			return strings.Replace(hcl, "  schema = schema.public\n", "  schema = schema.public\n  partial = true\n", 1)
		}
		return hcl
	}
	drops := []string{
		changeID("drop", "column", "tasks", "extra"),
		changeID("drop", "index", "tasks", "tasks_label_idx"),
		changeID("drop", "check", "tasks", "tasks_label_ck"),
	}
	ids := func(changes ...engine.Artifact) []string {
		var out []string
		for _, a := range changes {
			for _, c := range a.Changes {
				out = append(out, c.ID)
			}
		}
		return out
	}

	It("reads partial = true into the projection and keeps it out of the native snapshot", func() {
		partial, complete := desiredOf(declared(true, "text")), desiredOf(declared(false, "text"))
		Expect(partial.Tables[0].Partial).To(BeTrue())
		Expect(complete.Tables[0].Partial).To(BeFalse())
		Expect(partial.Native).To(Equal(complete.Native))
	})

	It("drops the undeclared column, index and check of a complete declaration, shown and acknowledged", func(ctx SpecContext) {
		a := mustPlan(ctx, db, scope(engine.ScopeDeclared, "tasks"), declared(false, "text"))
		Expect(a.Preserved).To(BeEmpty())
		Expect(ids(a)).To(ConsistOf(drops))
		Expect(engine.Acknowledgements(a)).To(ConsistOf(drops))
		Expect(statements(a)).To(ConsistOf(
			`DROP INDEX "public"."tasks_label_idx"`,
			`ALTER TABLE "public"."tasks" DROP CONSTRAINT "tasks_label_ck", DROP COLUMN "extra"`))
		Expect(mustApply(ctx, db, a).Outcome).To(Equal(engine.Applied))
		Expect(mustPlan(ctx, db, scope(engine.ScopeDeclared, "tasks"), declared(false, "text")).Units).To(BeEmpty())
	})

	DescribeTable("plans nothing for a partial declaration and lists the three drops as preserved",
		func(ctx SpecContext, mode engine.ScopeMode) {
			a := mustPlan(ctx, db, scope(mode, "tasks"), declared(true, "text"))
			Expect(a.Changes).To(BeEmpty())
			Expect(a.Units).To(BeEmpty())
			Expect(ids(engine.Artifact{Changes: a.Preserved})).To(ConsistOf(drops))
			Expect(engine.Validate(a)).To(Succeed())
		},
		Entry("declared scope", engine.ScopeDeclared),
		Entry("complete scope", engine.ScopeComplete),
	)

	It("still rebuilds the view over a column a partial declaration retypes, and keeps what it does not name", func(ctx SpecContext) {
		mustExec(db, "CREATE VIEW labels AS SELECT label FROM tasks")
		a := mustPlan(ctx, db, scope(engine.ScopeDeclared, "tasks"), declared(true, "varchar(20)"))
		Expect(ids(a)).To(ConsistOf(changeID("modify", "column", "tasks", "label"), ContainSubstring(`"rebuild","view"`)))
		Expect(ids(engine.Artifact{Changes: a.Preserved})).To(ConsistOf(drops))
		Expect(mustApply(ctx, db, a).Outcome).To(Equal(engine.Applied))

		var extra string
		Expect(db.QueryRow("SELECT extra FROM tasks WHERE id = 1").Scan(&extra)).To(Succeed())
		Expect(extra).To(Equal("dba value"))
		var views int
		Expect(db.QueryRow("SELECT count(*) FROM pg_views WHERE viewname = 'labels'").Scan(&views)).To(Succeed())
		Expect(views).To(Equal(1))
	})
})
