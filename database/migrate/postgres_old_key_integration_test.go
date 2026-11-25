//go:build integration

package migrate

import (
	"context"
	"database/sql"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const acmeDocumentsHCL = `schema "public" {}
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
}
`

// An unreviewed apply drops the old key of a complete table and leaves the
// column the table no longer declares in place, reporting it on every run.
var _ = Describe("PostgreSQL bundle apply over a table with an old key", Label("integration"), func() {
	var (
		dsn string
		db  *sql.DB
	)
	BeforeEach(func(ctx SpecContext) {
		dsn, db = newPostgresDatabase(ctx)
		mustExecAll(ctx, db, "CREATE TABLE acme_documents (id uuid NOT NULL PRIMARY KEY, root_id uuid NOT NULL, path_key text NOT NULL, input_hash text NOT NULL, legacy_note text)")
	})

	preview := func(ctx context.Context) Preview {
		p, err := PreviewBundle(ctx, db, "postgres", hclBundle(acmeDocumentsHCL, nil), WithDir("db"), WithName("acme/planner"))
		Expect(err).NotTo(HaveOccurred())
		return p
	}

	DescribeTable("drops the old key, adds the column and key, and reports only the kept column on a re-plan",
		func(ctx SpecContext, create, columns string) {
			mustExecAll(ctx, db, fmt.Sprintf(create, "acme_documents_root_path_input_key", columns))
			warning := `column "legacy_note" of table public.acme_documents is in the database but not declared`

			Expect(preview(ctx).Warnings).To(ContainElement(ContainSubstring(warning)))
			Expect(Apply(ctx, dsn, hclBundle(acmeDocumentsHCL, nil), WithDir("db"), WithName("acme/planner"))).To(Succeed())

			Expect(scalarOf(ctx, db, "SELECT count(*) FROM pg_indexes WHERE indexname = 'acme_documents_root_path_input_key'")).To(Equal("0"))
			Expect(scalarOf(ctx, db, "SELECT count(*) FROM pg_indexes WHERE indexname = 'acme_documents_root_path_digest_key'")).To(Equal("1"))
			Expect(scalarOf(ctx, db, "SELECT string_agg(column_name, ',' ORDER BY column_name) FROM information_schema.columns WHERE table_name = 'acme_documents'")).
				To(Equal("content_digest,id,input_hash,legacy_note,path_key,root_id"))
			again := preview(ctx)
			Expect(again.Warnings).To(ConsistOf(ContainSubstring(warning)))
			Expect(again.Artifact.Units).To(BeEmpty())
		},
		Entry("a unique constraint over declared columns", "ALTER TABLE acme_documents ADD CONSTRAINT %s UNIQUE (%s)", "root_id, path_key, input_hash"),
		Entry("a plain unique index over declared columns", "CREATE UNIQUE INDEX %s ON acme_documents (%s)", "root_id, path_key, input_hash"),
		Entry("a unique constraint over the undeclared column", "ALTER TABLE acme_documents ADD CONSTRAINT %s UNIQUE (%s)", "root_id, legacy_note"),
		Entry("a plain unique index over the undeclared column", "CREATE UNIQUE INDEX %s ON acme_documents (%s)", "root_id, legacy_note"),
	)
})
