package migrate

import (
	"database/sql"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"path/filepath"
	"testing/fstest"
)

var _ = Describe("migration preview", func() {
	It("projects SQL Server HCL and routine bundles into the native artifact model", func() {
		files := integrationBundle()
		cfg := resolveOptions(integrationOptions)
		realm, err := sqlServerTables(files, cfg)
		Expect(err).NotTo(HaveOccurred())
		Expect(addRoutines(files, cfg.routines, realm.Schemas[0])).To(Succeed())
		projected, err := planner.FromAtlas(realm)
		Expect(err).NotTo(HaveOccurred())
		Expect(projected.Tables[0].Indexes).To(ContainElement(HaveField("Name", "PK_acme_orders")))
		Expect(projected.Tables[1].Indexes).To(ContainElement(HaveField("Name", "PK_acme_order_lines")))
		Expect(projected.Tables[0].Columns[1].Default.Expression).To(Equal("N'open'"))
	})
	DescribeTable("classifies parsed scripts", func(text, kind string) {
		analysis := AnalyzeScript(text, "postgres")
		Expect(analysis.Kind).To(Equal(kind))
	},
		Entry("DML", "INSERT INTO items VALUES (1)", "dml"),
		Entry("DDL", "ALTER TABLE items ADD COLUMN note text", "ddl"),
		Entry("mixed", "CREATE TABLE items (id int); INSERT INTO items VALUES (1)", "mixed"),
		Entry("unknown", "this is not valid SQL", "unknown"),
	)
	It("warns about pre DDL and still produces a read-only advisory preview", func(ctx SpecContext) {
		db, err := sql.Open("sqlite", filepath.Join(GinkgoT().TempDir(), "preview.db"))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		files := fstest.MapFS{
			"schema.hcl": {Data: []byte(portableSchema)},
			"pre.sql":    {Data: []byte("-- phase: pre\nCREATE TABLE script_created (id integer)")},
		}
		preview, err := PreviewBundle(ctx, db, "sqlite", files)
		Expect(err).NotTo(HaveOccurred())
		Expect(preview.Scripts).To(HaveLen(1))
		Expect(preview.Scripts[0].Kind).To(Equal("ddl"))
		Expect(preview.Warnings).NotTo(BeEmpty())
		Expect(preview.Artifact).NotTo(BeNil())
		Expect(preview.Artifact.Advisory).NotTo(BeEmpty())
		var count int
		Expect(db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE type = 'table'").Scan(&count)).To(Succeed())
		Expect(count).To(BeZero())
	})
})
