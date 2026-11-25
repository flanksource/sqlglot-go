package sqlite_test

import (
	"database/sql"

	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// An unreviewed apply selects every planned change but the drops of undeclared
// columns. The old key of a table is dropped whatever form it has and whichever
// columns it covers, including the column that stays.
var _ = Describe("SQLite old keys dropped while an undeclared column stays", func() {
	const (
		desired = `schema "main" {}
table "acme_documents" {
  schema = schema.main
  column "id" { type = integer }
  column "root_id" { type = integer }
  column "input_hash" { type = text }
  column "content_digest" {
    type = text
    null = true
  }
  index "acme_documents_root_digest_key" {
    unique  = true
    columns = [column.root_id, column.content_digest]
  }
}`
		oldKey = "acme_documents_root_input_key"
	)
	keptColumn := changeID("drop", "column", "acme_documents", "legacy_note")
	oldKeyDrop := changeID("drop", "index", "acme_documents", oldKey)

	var db *sql.DB
	BeforeEach(func() {
		db = openDB("")
		mustExec(db, "CREATE TABLE acme_documents (id integer NOT NULL, root_id integer NOT NULL, input_hash text NOT NULL, legacy_note text)",
			"INSERT INTO acme_documents VALUES (1, 1, 'hash', 'kept')")
	})

	DescribeTable("drops the old key, keeps the column and its rows, and re-plans only the column",
		func(ctx SpecContext, setup ...string) {
			mustExec(db, setup...)
			full := mustPlan(ctx, db, declaredScope("acme_documents"), desired)
			var kept []string
			for _, c := range full.Changes {
				if c.Action != "drop" || c.Kind != "column" {
					kept = append(kept, c.ID)
				}
			}
			Expect(full.Changes).To(HaveLen(len(kept) + 1))

			a := mustPlan(ctx, db, declaredScope("acme_documents"), desired, kept...)
			Expect(changeIDs(a)).To(ContainElement(oldKeyDrop))
			Expect(changeIDs(a)).NotTo(ContainElement(keptColumn))
			mustApply(ctx, db, a, engine.Acknowledgements(a)...)

			var keys int
			Expect(db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type = 'index' AND name IN (?, 'acme_documents_root_digest_key')`, oldKey).Scan(&keys)).To(Succeed())
			Expect(keys).To(Equal(1), "only the new key remains")
			Expect(columnNames(db, "acme_documents")).To(ConsistOf("id", "root_id", "input_hash", "legacy_note", "content_digest"))
			var note string
			Expect(db.QueryRow(`SELECT legacy_note FROM acme_documents WHERE id = 1`).Scan(&note)).To(Succeed())
			Expect(note).To(Equal("kept"))
			Expect(changeIDs(mustPlan(ctx, db, declaredScope("acme_documents"), desired))).To(ConsistOf(keptColumn))
		},
		Entry("a plain unique index over declared columns", "CREATE UNIQUE INDEX "+oldKey+" ON acme_documents (root_id, input_hash)"),
		Entry("a plain unique index over the undeclared column", "CREATE UNIQUE INDEX "+oldKey+" ON acme_documents (root_id, legacy_note)"),
		Entry("a plain unique index over the undeclared column of a table the same plan rebuilds", "DROP TABLE acme_documents",
			"CREATE TABLE acme_documents (id integer NOT NULL, root_id integer NOT NULL, input_hash text NOT NULL DEFAULT 'x', legacy_note text)",
			"INSERT INTO acme_documents VALUES (1, 1, 'hash', 'kept')",
			"CREATE UNIQUE INDEX "+oldKey+" ON acme_documents (root_id, legacy_note)"),
	)
})
