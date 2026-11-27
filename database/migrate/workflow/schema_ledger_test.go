package workflow

import (
	"context"
	"path/filepath"
	"strings"
	"testing/fstest"
	"time"

	bundle "github.com/flanksource/sqlglot-go/database/migrate"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const ledgerItems = `schema "public" {}
table "items" {
  schema = schema.public
  column "id" { type = text }
  column "label" { type = text }
  primary_key { columns = [column.id] }
INDEX}`

var _ = Describe("a reviewed deploy and the schema ledger", func() {
	It("makes the next Apply of a recorded bundle plan again and repair what the deploy moved", func(ctx context.Context) {
		path := filepath.Join(GinkgoT().TempDir(), "target.db")
		recorded := fstest.MapFS{"schema.hcl": &fstest.MapFile{Data: []byte(strings.Replace(ledgerItems, "INDEX", "", 1))}}
		Expect(bundle.Apply(ctx, path, recorded)).To(Succeed())
		db := openSQLite(path)
		index := func() int {
			var count int
			Expect(db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type = 'index' AND name = 'items_label'`).Scan(&count)).To(Succeed())
			return count
		}

		moved := strings.Replace(ledgerItems, "INDEX", "  index \"items_label\" { columns = [column.label] }\n", 1)
		plan, err := Plan(ctx, db, Options{Dialect: "sqlite", Schema: "main", Source: Source{Kind: "bundle", Files: []File{{Path: "schema.hcl", Content: moved}}}})
		Expect(err).NotTo(HaveOccurred())
		_, err = Apply(ctx, db, ApplyOptions{Dialect: "sqlite", Schema: "main", Artifact: plan.Artifact, Acks: plan.Acks, LockTimeout: time.Second})
		Expect(err).NotTo(HaveOccurred())
		Expect(index()).To(Equal(1))

		Expect(bundle.Apply(ctx, path, recorded)).To(Succeed())
		Expect(index()).To(BeZero(), "the recorded bundle was planned again and dropped the index the deploy added")
	})

	It("keeps the migration's own bookkeeping tables when a pasted schema plans the complete scope", func(ctx context.Context) {
		path := filepath.Join(GinkgoT().TempDir(), "target.db")
		items := strings.Replace(ledgerItems, "INDEX", "", 1)
		Expect(bundle.Apply(ctx, path, fstest.MapFS{"schema.hcl": &fstest.MapFile{Data: []byte(items)}})).To(Succeed())
		db := openSQLite(path)
		tables := func() []string {
			rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type = 'table' ORDER BY name`)
			Expect(err).NotTo(HaveOccurred())
			var names []string
			for rows.Next() {
				var name string
				Expect(rows.Scan(&name)).To(Succeed())
				names = append(names, name)
			}
			Expect(rows.Err()).To(Succeed())
			Expect(rows.Close()).To(Succeed())
			return names
		}
		Expect(tables()).To(Equal([]string{"items", "schema_migration_schema"}))

		unchanged, err := Plan(ctx, db, Options{Dialect: "sqlite", Schema: "main", Source: Source{Kind: "hcl", Content: strings.ReplaceAll(items, "public", "main")}})
		Expect(err).NotTo(HaveOccurred())
		Expect(unchanged.Acks).To(BeEmpty())
		Expect(unchanged.Steps).To(BeEmpty(), "the ledger drop was the only difference, so nothing is planned")

		pasted := strings.ReplaceAll(strings.Replace(ledgerItems, "INDEX", "  index \"items_label\" { columns = [column.label] }\n", 1), "public", "main")
		plan, err := Plan(ctx, db, Options{Dialect: "sqlite", Schema: "main", Source: Source{Kind: "hcl", Content: pasted}})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Acks).To(BeEmpty(), "no drop is planned, so none needs acknowledging")
		_, err = Apply(ctx, db, ApplyOptions{Dialect: "sqlite", Schema: "main", Artifact: plan.Artifact, Acks: plan.Acks, LockTimeout: time.Second})
		Expect(err).NotTo(HaveOccurred())
		Expect(tables()).To(Equal([]string{"items", "schema_migration_schema"}), "the bundle ledger survived the complete-scope plan")
	})
})
