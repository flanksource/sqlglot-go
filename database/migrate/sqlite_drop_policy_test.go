package migrate

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing/fstest"

	"github.com/flanksource/sqlglot-go/database/target"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const dropPolicySchema = `schema "public" {}
table "items" {
  schema = schema.public
  column "id" { type = text }
  column "name" { type = text }
  primary_key { columns = [column.id] }
}`

var _ = Describe("SQLite HCL drop policy", func() {
	DescribeTable("applies the drop option through HCL reconciliation", func(ctx context.Context, allowDrops bool) {
		dsn := filepath.Join(GinkgoT().TempDir(), "policy.db")
		files := fstest.MapFS{"schema.hcl": &fstest.MapFile{Data: []byte(dropPolicySchema)}}
		Expect(Apply(ctx, dsn, files)).To(Succeed())
		target, err := dbtarget.Parse(dsn)
		Expect(err).NotTo(HaveOccurred())
		database, err := sql.Open("sqlite", target.DSN)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(database.Close)
		_, err = database.ExecContext(ctx, `ALTER TABLE items ADD COLUMN note TEXT;
INSERT INTO items VALUES ('item-1', 'retained', 'keep this');
CREATE TABLE other (value TEXT); INSERT INTO other VALUES ('retained')`)
		Expect(err).NotTo(HaveOccurred())
		files["schema.hcl"].Data = []byte(strings.Replace(dropPolicySchema,
			"  primary_key", "  check \"items_name_nonempty\" { expr = \"length(name) > 0\" }\n  primary_key", 1))
		var options []Option
		if allowDrops {
			options = append(options, WithDrops())
		}
		for range 2 {
			Expect(Apply(ctx, dsn, files, options...)).To(Succeed())
		}
		var columns, tables int
		Expect(database.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('items') WHERE name = 'note'`).Scan(&columns)).To(Succeed())
		Expect(database.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'other'`).Scan(&tables)).To(Succeed())
		if allowDrops {
			Expect([]int{columns, tables}).To(Equal([]int{0, 0}))
		} else {
			Expect([]int{columns, tables}).To(Equal([]int{1, 1}))
			var note string
			Expect(database.QueryRowContext(ctx, `SELECT note FROM items WHERE id = 'item-1'`).Scan(&note)).To(Succeed())
			Expect(note).To(Equal("keep this"))
		}
	}, Entry("default", false), Entry("WithDrops", true))
})
