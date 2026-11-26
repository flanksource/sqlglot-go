//go:build integration

package postgres_test

import (
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/flanksource/sqlglot-go/database/migrate/dialect/postgres"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const itemsHCL = `schema "public" {}
table "items" {
  schema = schema.public
  column "id" { type = integer }
  column "label" { type = text }
}`

const enumHCL = `schema "public" {}
enum "state" {
  schema = schema.public
  values = ["new", "done"]
}
table "jobs" {
  schema = schema.public
  column "id" { type = integer }
  column "state" { type = enum.state }
}`

var _ = Describe("PostgreSQL plans through the engine", func() {
	var db *sql.DB
	BeforeEach(func(ctx SpecContext) { db = openDB(ctx) })

	It("creates a table, adds a column, and re-plans to nothing", func(ctx SpecContext) {
		a := mustPlan(ctx, db, scope(engine.ScopeComplete), itemsHCL)
		Expect(statements(a)).To(Equal([]string{`CREATE TABLE "public"."items" ("id" integer NOT NULL, "label" text NOT NULL)`}))
		Expect(engine.Validate(a)).To(Succeed())
		Expect(mustApply(ctx, db, a).Outcome).To(Equal(engine.Applied))
		Expect(mustApply(ctx, db, a).Outcome).To(Equal(engine.AlreadyApplied))
		Expect(mustPlan(ctx, db, scope(engine.ScopeComplete), itemsHCL).Units).To(BeEmpty())

		mustExec(db, "INSERT INTO items VALUES (1, 'kept')")
		withNote := strings.Replace(itemsHCL, `  column "label" { type = text }`, "  column \"label\" { type = text }\n  column \"note\" {\n    type = text\n    null = true\n  }", 1)
		b := mustPlan(ctx, db, scope(engine.ScopeComplete), withNote)
		Expect(statements(b)).To(Equal([]string{`ALTER TABLE "public"."items" ADD COLUMN "note" text NULL`}))
		mustApply(ctx, db, b)
		var label string
		Expect(db.QueryRow("SELECT label FROM items").Scan(&label)).To(Succeed())
		Expect(label).To(Equal("kept"))
	})

	It("creates an enum before the table that uses it", func(ctx SpecContext) {
		a := mustPlan(ctx, db, scope(engine.ScopeComplete), enumHCL)
		Expect(statements(a)[0]).To(HavePrefix("CREATE TYPE"))
		mustApply(ctx, db, a)
		Expect(mustPlan(ctx, db, scope(engine.ScopeComplete), enumHCL).Units).To(BeEmpty())
	})

	DescribeTable("retypes a column under a dependent view and a view on that view",
		func(ctx SpecContext, mode engine.ScopeMode) {
			mustExec(db,
				"CREATE TABLE items (id integer NOT NULL, label varchar(20) NOT NULL)", "INSERT INTO items VALUES (1, 'one'), (2, 'two')",
				"CREATE VIEW item_labels AS SELECT id, label FROM items",
				"CREATE VIEW label_count AS SELECT count(*) AS n, count(label) AS labelled FROM item_labels",
				"GRANT SELECT ON item_labels TO PUBLIC", "COMMENT ON VIEW item_labels IS 'labels'")
			sc := scope(mode, "items")
			a := mustPlan(ctx, db, sc, itemsHCL)

			modify := changeID("modify", "column", "items", "label")
			rebuilds := map[string]string{}
			for _, c := range a.Changes {
				if c.Action == "rebuild" {
					Expect(c.Kind).To(Equal("view"))
					rebuilds[c.Object.Name] = c.Cause
				}
			}
			Expect(rebuilds).To(Equal(map[string]string{"item_labels": modify, "label_count": modify}))
			Expect(a.Dependents).To(HaveLen(2))
			Expect(a.Units).To(HaveLen(1))

			stmts := statements(a)
			at := func(prefix string) int {
				return slices.IndexFunc(stmts, func(s string) bool { return strings.HasPrefix(s, prefix) })
			}
			Expect(at(`DROP VIEW "public"."label_count"`)).To(BeNumerically("<", at(`DROP VIEW "public"."item_labels"`)))
			Expect(at(`DROP VIEW "public"."item_labels"`)).To(BeNumerically("<", at(`ALTER TABLE "public"."items"`)))
			Expect(at(`ALTER TABLE "public"."items"`)).To(BeNumerically("<", at(`CREATE VIEW "public"."item_labels"`)))
			Expect(at(`CREATE VIEW "public"."item_labels"`)).To(BeNumerically("<", at(`CREATE VIEW "public"."label_count"`)))

			Expect(mustApply(ctx, db, a).Outcome).To(Equal(engine.Applied))
			var n int
			var labelled int
			var typ string
			Expect(db.QueryRow("SELECT n, labelled FROM label_count").Scan(&n, &labelled)).To(Succeed())
			Expect([]int{n, labelled}).To(Equal([]int{2, 2}))
			Expect(db.QueryRow("SELECT data_type FROM information_schema.columns WHERE table_name = 'items' AND column_name = 'label'").Scan(&typ)).To(Succeed())
			Expect(typ).To(Equal("text"))
			var comment string
			var granted bool
			Expect(db.QueryRow("SELECT obj_description('item_labels'::regclass, 'pg_class'), has_table_privilege('public', 'item_labels', 'SELECT')").Scan(&comment, &granted)).To(Succeed())
			Expect(comment).To(Equal("labels"))
			Expect(granted).To(BeTrue())
			Expect(mustPlan(ctx, db, sc, itemsHCL).Units).To(BeEmpty())
		},
		Entry("complete scope", engine.ScopeComplete),
		Entry("declared scope, views outside it", engine.ScopeDeclared),
	)

	It("honours the lock timeout while another migration holds the lock", func(ctx SpecContext) {
		a := mustPlan(ctx, db, scope(engine.ScopeComplete), itemsHCL)
		tx, err := db.BeginTx(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = tx.Rollback() })
		_, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(7561364747649)")
		Expect(err).NotTo(HaveOccurred())
		started := time.Now()
		_, err = apply(ctx, db, a, 100*time.Millisecond)
		Expect(err).To(MatchError(ContainSubstring("acquire migration lock")))
		Expect(time.Since(started)).To(BeNumerically("<", 5*time.Second))
		Expect(tx.Rollback()).To(Succeed())
		Expect(mustApply(ctx, db, a).Outcome).To(Equal(engine.Applied))
	})

	It("refuses objects one by one so an untouched function or view never blocks a plan, and keeps a table with a trigger", func(ctx SpecContext) {
		mustExec(db,
			"CREATE TABLE items (id integer NOT NULL, label text NOT NULL)", "CREATE TABLE audited (id integer NOT NULL)",
			"CREATE FUNCTION one() RETURNS integer LANGUAGE sql AS 'SELECT 1'",
			"CREATE FUNCTION audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$",
			"CREATE TRIGGER audited_log BEFORE INSERT ON audited FOR EACH ROW EXECUTE FUNCTION audit()",
			"CREATE VIEW item_ids AS SELECT id FROM items")
		insp, err := postgres.Dialect.Inspect(ctx, db, scope(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		refused := []string{}
		for _, r := range insp.Refusals {
			refused = append(refused, r.Object.Name)
		}
		Expect(refused).To(ConsistOf("one", "audit"))
		Expect(insp.Schema.Tables).To(HaveLen(2))
		Expect(insp.Schema.Definitions).To(ConsistOf(HaveField("Name", migrate.Name{Schema: "public", Name: "item_ids"})))

		withNote := strings.Replace(itemsHCL, `  column "label" { type = text }`, "  column \"label\" { type = text }\n  column \"note\" {\n    type = text\n    null = true\n  }", 1)
		a := mustPlan(ctx, db, scope(engine.ScopeDeclared, "items"), withNote)
		Expect(statements(a)).To(Equal([]string{`ALTER TABLE "public"."items" ADD COLUMN "note" text NULL`}))
	})

	It("creates tables with a foreign key, then changes the child alone while its parent is outside the declared scope", func(ctx SpecContext) {
		const family = `schema "public" {}
table "child" {
  schema = schema.public
  column "id" { type = integer }
  column "parent_id" { type = integer }
  primary_key {
    columns = [column.id]
  }
  foreign_key "child_parent_fk" {
    columns = [column.parent_id]
    ref_columns = [table.parent.column.id]
  }
}
table "parent" {
  schema = schema.public
  column "id" { type = integer }
  primary_key {
    columns = [column.id]
  }
}`
		mustApply(ctx, db, mustPlan(ctx, db, scope(engine.ScopeComplete), family))
		Expect(mustPlan(ctx, db, scope(engine.ScopeComplete), family).Units).To(BeEmpty())
		// The desired schema declares the whole family; the plan narrows it to the declared scope
		// while its native snapshot keeps the parent the foreign key names.
		withNote := desiredOf(strings.Replace(family, `  column "parent_id" { type = integer }`, "  column \"parent_id\" { type = integer }\n  column \"note\" {\n    type = text\n    null = true\n  }", 1))
		a, err := engine.PlanLive(ctx, db, engine.PlanLiveRequest{Dialect: postgres.Name, Scope: scope(engine.ScopeDeclared, "child"), Desired: withNote})
		Expect(err).NotTo(HaveOccurred())
		Expect(statements(a)).To(Equal([]string{`ALTER TABLE "public"."child" ADD COLUMN "note" text NULL`}))
		Expect(mustApply(ctx, db, a).Outcome).To(Equal(engine.Applied))
	})

	It("creates a parent before the foreign key that references it when a cycle elsewhere splits every foreign key out", func(ctx SpecContext) {
		const cyclic = `schema "public" {}
table "plans" {
  schema = schema.public
  column "id" { type = integer }
  column "revision_id" {
    type = integer
    null = true
  }
  primary_key { columns = [column.id] }
  foreign_key "plans_revision_id_fkey" {
    columns = [column.revision_id]
    ref_columns = [table.revisions.column.id]
  }
}
table "revisions" {
  schema = schema.public
  column "id" { type = integer }
  column "plan_id" { type = integer }
  primary_key { columns = [column.id] }
  foreign_key "revisions_plan_id_fkey" {
    columns = [column.plan_id]
    ref_columns = [table.plans.column.id]
  }
}
table "children" {
  schema = schema.public
  column "id" { type = integer }
  column "owner_id" { type = integer }
  primary_key { columns = [column.id] }
  foreign_key "children_owner_id_fkey" {
    columns = [column.owner_id]
    ref_columns = [table.owners.column.id]
  }
}
table "owners" {
  schema = schema.public
  column "id" { type = integer }
  primary_key { columns = [column.id] }
}`
		a := mustPlan(ctx, db, scope(engine.ScopeComplete), cyclic)
		Expect(mustApply(ctx, db, a).Outcome).To(Equal(engine.Applied))
		Expect(mustPlan(ctx, db, scope(engine.ScopeComplete), cyclic).Units).To(BeEmpty())
	})

	It("blocks a NOT NULL change while NULL values exist", func(ctx SpecContext) {
		mustExec(db, "CREATE TABLE items (id integer NOT NULL, label text)", "INSERT INTO items VALUES (1, NULL)")
		a := mustPlan(ctx, db, scope(engine.ScopeComplete), itemsHCL)
		var pre *engine.PreconditionError
		_, err := apply(ctx, db, a, 0)
		Expect(errors.As(err, &pre)).To(BeTrue(), "got %v", err)
		Expect(pre.Message).To(ContainSubstring("NULL"))
	})

	It("reports drift on a declared table that changed after the preview", func(ctx SpecContext) {
		mustExec(db, "CREATE TABLE items (id integer NOT NULL)")
		a := mustPlan(ctx, db, scope(engine.ScopeDeclared, "items"), itemsHCL)
		mustExec(db, "ALTER TABLE items ADD COLUMN intervening text")
		_, err := apply(ctx, db, a, 0)
		var drift *engine.DriftError
		Expect(errors.As(err, &drift)).To(BeTrue(), "got %v", err)
	})

	It("retypes under a view whose stored text changes with the type, because the cast it printed became redundant", func(ctx SpecContext) {
		mustExec(db,
			"CREATE TABLE items (id integer NOT NULL, label varchar(20) NOT NULL)", "INSERT INTO items VALUES (1, 'one'), (2, 'two')",
			"CREATE VIEW item_labels AS SELECT id, label FROM items",
			"CREATE VIEW label_top AS SELECT max(label) AS top FROM item_labels")
		var before string
		Expect(db.QueryRow("SELECT pg_get_viewdef('label_top'::regclass, true)").Scan(&before)).To(Succeed())
		Expect(before).To(ContainSubstring("label::text"))

		a := mustPlan(ctx, db, scope(engine.ScopeComplete), itemsHCL)
		Expect(mustApply(ctx, db, a).Outcome).To(Equal(engine.Applied))

		var after, top string
		Expect(db.QueryRow("SELECT pg_get_viewdef('label_top'::regclass, true)").Scan(&after)).To(Succeed())
		Expect(after).NotTo(Equal(before), "the stored text really changed")
		Expect(db.QueryRow("SELECT top FROM label_top").Scan(&top)).To(Succeed())
		Expect(top).To(Equal("two"))
		Expect(mustPlan(ctx, db, scope(engine.ScopeComplete), itemsHCL).Units).To(BeEmpty())
	})
})
