//go:build integration

package postgres_test

import (
	"database/sql"
	"fmt"

	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// An unreviewed apply selects every planned change but the drops of undeclared
// columns. The old key of a table is dropped whatever form it has and whichever
// columns it covers, including the column that stays.
var _ = Describe("PostgreSQL old keys dropped while an undeclared column stays", func() {
	const desired = `schema "public" {}
table "acme_documents" {
  schema = schema.public
  column "id" { type = uuid }
  column "root_id" { type = uuid }
  column "path_key" { type = text }
  column "input_hash" { type = text }
  column "content_digest" {
    type = text
    null = true
  }
  primary_key { columns = [column.id] }
  unique "acme_documents_root_path_digest_key" { columns = [column.root_id, column.path_key, column.content_digest] }
}`
	oldKey := "acme_documents_root_path_input_key"
	keptColumn := changeID("drop", "column", "acme_documents", "legacy_note")
	oldKeyDrop := changeID("drop", "index", "acme_documents", oldKey)

	var db *sql.DB
	BeforeEach(func(ctx SpecContext) {
		db = openDB(ctx)
		mustExec(db, `CREATE TABLE acme_documents (id uuid NOT NULL PRIMARY KEY, root_id uuid NOT NULL, path_key text NOT NULL, input_hash text NOT NULL, legacy_note text)`)
	})

	// unreviewed plans the way an unreviewed apply does: every change but the column drops.
	unreviewed := func(ctx SpecContext) engine.Artifact {
		full := mustPlan(ctx, db, scope(engine.ScopeDeclared, "acme_documents"), desired)
		var kept []string
		for _, c := range full.Changes {
			if c.Action != "drop" || c.Kind != "column" {
				kept = append(kept, c.ID)
			}
		}
		Expect(kept).To(HaveLen(len(full.Changes)-1), "the plan drops the one undeclared column")
		a, err := engine.PlanLive(ctx, db, engine.PlanLiveRequest{
			Dialect: "postgres", Scope: scope(engine.ScopeDeclared, "acme_documents"), Desired: desiredOf(desired), Selection: kept,
		})
		Expect(err).NotTo(HaveOccurred())
		return a
	}

	DescribeTable("drops the old key, keeps the column and re-plans only the column",
		func(ctx SpecContext, create, columns string) {
			mustExec(db, fmt.Sprintf(create, oldKey, columns))
			a := unreviewed(ctx)
			Expect(changeIDs(a)).To(ContainElement(oldKeyDrop))
			Expect(changeIDs(a)).NotTo(ContainElement(keptColumn))
			Expect(statements(a)).To(ContainElement(ContainSubstring(oldKey)), "a statement drops the key; no empty op stands in for it")
			Expect(mustApply(ctx, db, a).Outcome).To(Equal(engine.Applied))

			var left, columnsLeft int
			Expect(db.QueryRow(`SELECT count(*) FROM pg_indexes WHERE indexname = $1`, oldKey).Scan(&left)).To(Succeed())
			Expect(left).To(BeZero(), "the old key is gone")
			Expect(db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_name = 'acme_documents' AND column_name IN ('legacy_note', 'content_digest')`).Scan(&columnsLeft)).To(Succeed())
			Expect(columnsLeft).To(Equal(2), "the undeclared column stays and the declared one is added")
			Expect(changeIDs(mustPlan(ctx, db, scope(engine.ScopeDeclared, "acme_documents"), desired))).To(ConsistOf(keptColumn))
		},
		Entry("a unique constraint over declared columns", `ALTER TABLE acme_documents ADD CONSTRAINT %s UNIQUE (%s)`, "root_id, path_key, input_hash"),
		Entry("a plain unique index over declared columns", `CREATE UNIQUE INDEX %s ON acme_documents (%s)`, "root_id, path_key, input_hash"),
		Entry("a unique constraint over the undeclared column", `ALTER TABLE acme_documents ADD CONSTRAINT %s UNIQUE (%s)`, "root_id, legacy_note"),
		Entry("a plain unique index over the undeclared column", `CREATE UNIQUE INDEX %s ON acme_documents (%s)`, "root_id, legacy_note"),
	)
})

func changeIDs(a engine.Artifact) []string {
	out := []string{}
	for _, c := range a.Changes {
		out = append(out, c.ID)
	}
	return out
}
