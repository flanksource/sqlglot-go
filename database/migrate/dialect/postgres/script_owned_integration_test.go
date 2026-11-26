//go:build integration

package postgres_test

import (
	"database/sql"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Triggers are created by a bundle's post scripts, not its HCL, so a table
// carrying one must still be planned. Only a change PostgreSQL itself refuses
// while a trigger reads the column (a retype or drop of a column in the
// trigger's UPDATE OF list or WHEN clause) fails the plan.
var _ = Describe("PostgreSQL tables with triggers", func() {
	const audited = `schema "public" {}
table "audited" {
  schema = schema.public
  column "id" { type = integer }
  column "state" { type = text }
  column "note" { type = text }
  column "label" { type = text }
  primary_key { columns = [column.id] }
}`
	const stateTrigger = "audited_state"

	var db *sql.DB
	BeforeEach(func(ctx SpecContext) {
		db = openDB(ctx)
		mustExec(db,
			"CREATE TABLE audited (id integer NOT NULL PRIMARY KEY, state text NOT NULL, note text NOT NULL, label text NOT NULL)",
			"CREATE FUNCTION touch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$",
			"CREATE TRIGGER audited_touch BEFORE UPDATE ON audited FOR EACH ROW EXECUTE FUNCTION touch()",
			"CREATE TRIGGER "+stateTrigger+" BEFORE UPDATE OF state ON audited FOR EACH ROW WHEN (OLD.note IS DISTINCT FROM NEW.note) EXECUTE FUNCTION touch()")
	})

	triggerCount := func() int {
		var n int
		Expect(db.QueryRow("SELECT count(*) FROM pg_trigger WHERE tgrelid = 'audited'::regclass AND NOT tgisinternal").Scan(&n)).To(Succeed())
		return n
	}

	It("re-plans the table to nothing", func(ctx SpecContext) {
		Expect(mustPlan(ctx, db, scope(engine.ScopeComplete), audited).Units).To(BeEmpty())
	})

	It("creates a table whose foreign key references the table with triggers", func(ctx SpecContext) {
		withLinks := audited + `
table "audit_links" {
  schema = schema.public
  column "id" { type = integer }
  column "audited_id" { type = integer }
  primary_key { columns = [column.id] }
  foreign_key "audit_links_audited_id_fkey" {
    columns = [column.audited_id]
    ref_columns = [table.audited.column.id]
  }
}`
		a := mustPlan(ctx, db, scope(engine.ScopeComplete), withLinks)
		Expect(a.Changes).To(ConsistOf(HaveField("ID", changeID("add", "table", "audit_links", ""))))
		Expect(mustApply(ctx, db, a).Outcome).To(Equal(engine.Applied))
		Expect(mustPlan(ctx, db, scope(engine.ScopeComplete), withLinks).Units).To(BeEmpty())
	})

	DescribeTable("applies a change to a column no trigger reads and keeps the triggers",
		func(ctx SpecContext, desired string) {
			a := mustPlan(ctx, db, scope(engine.ScopeDeclared, "audited"), desired)
			Expect(a.Units).NotTo(BeEmpty())
			Expect(mustApply(ctx, db, a).Outcome).To(Equal(engine.Applied))
			Expect(triggerCount()).To(Equal(2))
			Expect(mustPlan(ctx, db, scope(engine.ScopeDeclared, "audited"), desired).Units).To(BeEmpty())
		},
		Entry("an added column", strings.Replace(audited, `  column "label" { type = text }`,
			"  column \"label\" { type = text }\n  column \"extra\" {\n    type = text\n    null = true\n  }", 1)),
		Entry("a retyped column", strings.Replace(audited, `column "label" { type = text }`, `column "label" { type = varchar(40) }`, 1)),
		Entry("a dropped column", strings.Replace(audited, "  column \"label\" { type = text }\n", "", 1)),
	)

	DescribeTable("refuses a change to a column a trigger reads, naming the trigger and its definition",
		func(ctx SpecContext, desired, change string) {
			_, err := planLive(ctx, db, scope(engine.ScopeDeclared, "audited"), desired)
			Expect(err).To(MatchError(And(
				ContainSubstring("cannot rebuild trigger public.audited."+stateTrigger),
				ContainSubstring(change),
				ContainSubstring("CREATE TRIGGER "+stateTrigger),
			)))
		},
		Entry("retyping a column of the UPDATE OF list",
			strings.Replace(audited, `column "state" { type = text }`, `column "state" { type = varchar(40) }`, 1),
			changeID("modify", "column", "audited", "state")),
		Entry("retyping a column of the WHEN clause",
			strings.Replace(audited, `column "note" { type = text }`, `column "note" { type = varchar(40) }`, 1),
			changeID("modify", "column", "audited", "note")),
		Entry("dropping a column of the WHEN clause",
			strings.Replace(audited, "  column \"note\" { type = text }\n", "", 1),
			changeID("drop", "column", "audited", "note")),
	)

	It("drops the table with its triggers", func(ctx SpecContext) {
		a := mustPlan(ctx, db, scope(engine.ScopeComplete), `schema "public" {}`)
		Expect(a.Changes).To(ConsistOf(HaveField("ID", changeID("drop", "table", "audited", ""))))
		Expect(mustApply(ctx, db, a).Outcome).To(Equal(engine.Applied))
		var exists bool
		Expect(db.QueryRow("SELECT to_regclass('public.audited') IS NOT NULL").Scan(&exists)).To(Succeed())
		Expect(exists).To(BeFalse())
	})
})

// A script can make a constraint the HCL declares DEFERRABLE, which the native
// snapshot cannot hold. The table stays managed; only a change to that
// constraint is refused.
var _ = Describe("PostgreSQL tables with a deferrable constraint", func() {
	const fkName = "children_owner_id_fkey"
	const family = `schema "public" {}
table "owners" {
  schema = schema.public
  column "id" { type = integer }
  primary_key { columns = [column.id] }
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
}`

	var db *sql.DB
	BeforeEach(func(ctx SpecContext) {
		db = openDB(ctx)
		mustApply(ctx, db, mustPlan(ctx, db, scope(engine.ScopeComplete), family))
		mustExec(db, "ALTER TABLE children ALTER CONSTRAINT "+fkName+" DEFERRABLE INITIALLY DEFERRED")
	})

	deferrable := func() bool {
		var d bool
		Expect(db.QueryRow("SELECT condeferrable FROM pg_constraint WHERE conname = $1", fkName).Scan(&d)).To(Succeed())
		return d
	}

	It("re-plans to nothing", func(ctx SpecContext) {
		Expect(mustPlan(ctx, db, scope(engine.ScopeComplete), family).Units).To(BeEmpty())
	})

	It("adds a column to the table and keeps the constraint deferrable", func(ctx SpecContext) {
		withNote := strings.Replace(family, `  column "owner_id" { type = integer }`,
			"  column \"owner_id\" { type = integer }\n  column \"note\" {\n    type = text\n    null = true\n  }", 1)
		a := mustPlan(ctx, db, scope(engine.ScopeComplete), withNote)
		Expect(statements(a)).To(Equal([]string{`ALTER TABLE "public"."children" ADD COLUMN "note" text NULL`}))
		Expect(mustApply(ctx, db, a).Outcome).To(Equal(engine.Applied))
		Expect(deferrable()).To(BeTrue())
	})

	It("refuses a change to the deferrable constraint, naming it", func(ctx SpecContext) {
		cascading := strings.Replace(family, "    ref_columns = [table.owners.column.id]\n",
			"    ref_columns = [table.owners.column.id]\n    on_delete = CASCADE\n", 1)
		_, err := planLive(ctx, db, scope(engine.ScopeComplete), cascading)
		Expect(err).To(MatchError(And(
			ContainSubstring("touches public.children."+fkName+", which inspection refused"),
			ContainSubstring("deferrable"),
		)))
		Expect(deferrable()).To(BeTrue())
	})
})
