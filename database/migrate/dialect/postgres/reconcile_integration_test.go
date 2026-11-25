//go:build integration

package postgres_test

import (
	"database/sql"
	"strings"

	"github.com/flanksource/sqlglot-go/database/migrate/dialect/postgres"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func quantityHCL(expression string) string {
	return `schema "public" {}
table "items" {
  schema = schema.public
  column "id" { type = integer }
  column "qty" {
    type = integer
    default = sql("` + expression + `")
  }
}`
}

func changeIDsOf(d engine.Diff) []string {
	ids := []string{}
	for _, c := range d.Changes() {
		ids = append(ids, c.ID)
	}
	return ids
}

var _ = Describe("PostgreSQL reconciling column defaults", func() {
	var (
		db      *sql.DB
		current migrate.Schema
	)
	BeforeEach(func(ctx SpecContext) {
		db = openDB(ctx)
		mustExec(db, "CREATE TABLE items (id integer NOT NULL, qty integer NOT NULL DEFAULT 2)")
		insp, err := postgres.Dialect.Inspect(ctx, db, scope(engine.ScopeComplete))
		Expect(err).NotTo(HaveOccurred())
		current = insp.Schema
	})

	diffOf := func(desired migrate.Schema) []string {
		d, err := postgres.Dialect.Diff(current, desired)
		Expect(err).NotTo(HaveOccurred())
		return changeIDsOf(d)
	}

	It("spells an equal but differently written default like the live one", func(ctx SpecContext) {
		desired := desiredOf(quantityHCL("1+1"))
		Expect(diffOf(desired)).To(Equal([]string{changeID("modify", "column", "items", "qty")}), "offline, the spelling alone is a change")

		reconciled, err := postgres.Dialect.Reconcile(ctx, db, current, desired)
		Expect(err).NotTo(HaveOccurred())
		Expect(diffOf(reconciled)).To(BeEmpty())
		Expect(reconciled.Tables[0].Columns[1].Default).To(Equal(&migrate.Default{Expression: "2"}))
		Expect(reconciled.Native).To(ContainSubstring("default = 2"))
		Expect(mustPlan(ctx, db, scope(engine.ScopeComplete), quantityHCL("1+1")).Units).To(BeEmpty())
	})

	It("leaves a genuinely different default as a change", func(ctx SpecContext) {
		desired := desiredOf(quantityHCL("1+2"))
		reconciled, err := postgres.Dialect.Reconcile(ctx, db, current, desired)
		Expect(err).NotTo(HaveOccurred())
		Expect(reconciled).To(Equal(desired))
		Expect(diffOf(reconciled)).To(Equal([]string{changeID("modify", "column", "items", "qty")}))
	})

	It("is idempotent", func(ctx SpecContext) {
		once, err := postgres.Dialect.Reconcile(ctx, db, current, desiredOf(quantityHCL("1+1")))
		Expect(err).NotTo(HaveOccurred())
		twice, err := postgres.Dialect.Reconcile(ctx, db, current, once)
		Expect(err).NotTo(HaveOccurred())
		Expect(twice).To(Equal(once))
	})

	It("reconciles a table of a narrowed schema without touching the Native tables it left out", func(ctx SpecContext) {
		both := strings.Replace(quantityHCL("1+1"), "\n}", "\n}\ntable \"other\" {\n  schema = schema.public\n  column \"id\" { type = integer }\n  column \"qty\" {\n    type = integer\n    default = sql(\"1+1\")\n  }\n}", 1)
		desired := desiredOf(both)
		desired.Tables = desired.Tables[:1]
		Expect(desired.Tables[0].Name.Name).To(Equal("items"))
		reconciled, err := postgres.Dialect.Reconcile(ctx, db, current, desired)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Count(reconciled.Native, "default = 2")).To(Equal(1), "only the table in the projection is rewritten")
	})
})
