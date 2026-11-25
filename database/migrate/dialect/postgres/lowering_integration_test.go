//go:build integration

package postgres_test

import (
	"database/sql"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const tasksHCL = `schema "public" {}
enum "state" {
  schema = schema.public
  values = [VALUES]
}
table "tasks" {
  schema = schema.public
  column "id" { type = integer }
  column "state" { type = enum.state }
  EXTRA
}
`

func tasksOf(values, extra string) string {
	return strings.NewReplacer("VALUES", values, "EXTRA", extra).Replace(tasksHCL)
}

var _ = Describe("PostgreSQL lowering of changes the planner reports loosely", func() {
	var db *sql.DB
	BeforeEach(func(ctx SpecContext) { db = openDB(ctx) })

	It("adds enum values in place, before a column of the type, in a unit of its own", func(ctx SpecContext) {
		mustExec(db, "CREATE TYPE state AS ENUM ('new', 'done')", "CREATE TABLE tasks (id integer NOT NULL, state state NOT NULL)", "INSERT INTO tasks VALUES (1, 'done')")
		desired := tasksOf(`"new", "doing", "done"`, "column \"was\" {\n    type = enum.state\n    null = true\n  }")

		a := mustPlan(ctx, db, scope(engine.ScopeDeclared, "tasks", "state"), desired)
		Expect(statements(a)).To(Equal([]string{
			`ALTER TYPE "public"."state" ADD VALUE 'doing' AFTER 'new'`,
			`ALTER TABLE "public"."tasks" ADD COLUMN "was" "public"."state" NULL`,
		}))
		Expect(mustApply(ctx, db, a).Outcome).To(Equal(engine.Applied))
		Expect(mustPlan(ctx, db, scope(engine.ScopeDeclared, "tasks", "state"), desired).Units).To(BeEmpty())
	})

	It("keeps an edited check in the declared scope, and reads an equivalent spelling as no edit", func(ctx SpecContext) {
		mustExec(db, "CREATE TABLE tasks (id integer NOT NULL, state text NOT NULL, CONSTRAINT state_known CHECK (state IN ('a', 'b')))")
		const base = `schema "public" {}
table "tasks" {
  schema = schema.public
  column "id" { type = integer }
  column "state" { type = text }
  check "state_known" { expr = "EXPR" }
}`
		plan := func(expr string) engine.Artifact {
			return mustPlan(ctx, db, scope(engine.ScopeDeclared, "tasks"), strings.Replace(base, "EXPR", expr, 1))
		}
		Expect(plan("state IN ('a', 'b')").Units).To(BeEmpty())

		edited := plan("state IN ('a', 'b', 'c')")
		Expect(edited.Preserved).To(BeEmpty())
		Expect(statements(edited)).To(HaveLen(1))
		Expect(statements(edited)[0]).To(ContainSubstring(`DROP CONSTRAINT "state_known"`))
		Expect(mustApply(ctx, db, edited).Outcome).To(Equal(engine.Applied))
		Expect(plan("state IN ('a', 'b', 'c')").Units).To(BeEmpty())
	})

	It("drops a column together with the index on it, which the database removes with the column", func(ctx SpecContext) {
		mustExec(db, "CREATE TABLE tasks (id integer NOT NULL, state text NOT NULL, note text)", "CREATE INDEX tasks_note ON tasks (note)")
		a := mustPlan(ctx, db, scope(engine.ScopeComplete), `schema "public" {}
table "tasks" {
  schema = schema.public
  column "id" { type = integer }
  column "state" { type = text }
}`)
		Expect(artifactChangeIDs(a)).To(ConsistOf(changeID("drop", "column", "tasks", "note"), changeID("drop", "index", "tasks", "tasks_note")))
		Expect(statements(a)).To(Equal([]string{`ALTER TABLE "public"."tasks" DROP COLUMN "note"`}))
		Expect(mustApply(ctx, db, a).Outcome).To(Equal(engine.Applied))
		Expect(mustPlan(ctx, db, scope(engine.ScopeComplete), `schema "public" {}
table "tasks" {
  schema = schema.public
  column "id" { type = integer }
  column "state" { type = text }
}`).Units).To(BeEmpty())
	})
})

const dropFixture = `schema "public" {}
table "t" {
  schema = schema.public
  column "id" { type = integer }
  COLUMNS
}`

func dropDesired(columns string) string { return strings.Replace(dropFixture, "COLUMNS", columns, 1) }

var _ = Describe("PostgreSQL drops under dependent views", func() {
	var db *sql.DB
	BeforeEach(func(ctx SpecContext) {
		db = openDB(ctx)
		mustExec(db, "CREATE TABLE t (id integer NOT NULL, c text, d text)", "INSERT INTO t VALUES (1, 'c', 'd')")
	})
	viewNames := func() string {
		var names string
		Expect(db.QueryRow("SELECT coalesce(string_agg(viewname, ',' ORDER BY viewname), '') FROM pg_views WHERE schemaname = 'public'").Scan(&names)).To(Succeed())
		return names
	}
	columns := func() string {
		var names string
		Expect(db.QueryRow("SELECT string_agg(column_name, ',' ORDER BY ordinal_position) FROM information_schema.columns WHERE table_name = 't'").Scan(&names)).To(Succeed())
		return names
	}

	It("refuses a column drop under a view and a view on that view at plan time, naming the first with its definition, and changes nothing", func(ctx SpecContext) {
		mustExec(db, "CREATE VIEW v1 AS SELECT id, c FROM t", "CREATE VIEW v2 AS SELECT id FROM v1")

		_, err := planLive(ctx, db, scope(engine.ScopeComplete), dropDesired(`column "d" {
    type = text
    null = true
  }`))
		Expect(err).To(MatchError(ContainSubstring("cannot rebuild view public.v1 (forced by ")))
		Expect(err).To(MatchError(ContainSubstring("view public.v1 reads column c of table public.t, which the plan drops")))
		Expect(err).To(MatchError(ContainSubstring(`CREATE VIEW "public"."v1" ("id", "c") AS`)))
		Expect(err).NotTo(MatchError(ContainSubstring("public.v2 reads")))
		Expect(columns()).To(Equal("id,c,d"))
		Expect(viewNames()).To(Equal("v1,v2"))
	})

	It("does not let a view on other columns of the table block the drop", func(ctx SpecContext) {
		mustExec(db, "CREATE VIEW v3 AS SELECT id, d FROM t")
		a := mustPlan(ctx, db, scope(engine.ScopeComplete), dropDesired(`column "d" {
    type = text
    null = true
  }`))
		Expect(mustApply(ctx, db, a).Outcome).To(Equal(engine.Applied))
		Expect(columns()).To(Equal("id,d"))
		Expect(viewNames()).To(Equal("v3"))
	})

	It("refuses a table drop under a view, whether it reads a column or only counts rows", func(ctx SpecContext) {
		for _, view := range []string{"CREATE VIEW v1 AS SELECT id, c FROM t", "CREATE VIEW v4 AS SELECT count(*) AS n FROM t"} {
			mustExec(db, view)
			_, err := planLive(ctx, db, scope(engine.ScopeComplete), `schema "public" {}`)
			Expect(err).To(MatchError(ContainSubstring("cannot rebuild view public.")))
			Expect(err).To(MatchError(ContainSubstring("which the plan drops")))
			Expect(err).To(MatchError(ContainSubstring("CREATE VIEW")))
			mustExec(db, "DROP VIEW "+strings.Fields(view)[2])
		}
		Expect(columns()).To(Equal("id,c,d"))
	})

	It("still keeps a drop under a view when the scope is declared", func(ctx SpecContext) {
		mustExec(db, "CREATE VIEW v1 AS SELECT id, c FROM t")
		a := mustPlan(ctx, db, scope(engine.ScopeDeclared, "t"), dropDesired(`column "c" {
    type = text
    null = true
  }
  column "d" {
    type = text
    null = true
  }`))
		Expect(a.Units).To(BeEmpty())
	})
})

func artifactChangeIDs(a engine.Artifact) []string {
	ids := []string{}
	for _, c := range a.Changes {
		ids = append(ids, c.ID)
	}
	return ids
}
