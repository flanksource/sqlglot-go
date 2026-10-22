package review

import (
	"database/sql"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	_ "modernc.org/sqlite"
	"path/filepath"
	"testing"
)

func TestReviewedSchema(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Reviewed schema migrations")
}

const itemsHCL = `schema "main" {}
table "items" {
  schema = schema.main
  column "id" { type = integer }
  column "label" { type = text }
}`

var _ = Describe("SQLite reviewed artifacts", func() {
	var db *sql.DB
	BeforeEach(func() {
		var err error
		db, err = sql.Open("sqlite", filepath.Join(GinkgoT().TempDir(), "review.db"))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
	})
	It("produces the same reviewed hash for reordered named declarations", func(ctx SpecContext) {
		first := `table "first" {
 schema = schema.main
 column "id" { type = integer }
}`
		second := `table "second" {
 schema = schema.main
 column "id" { type = integer }
}`
		a, err := Build(ctx, db, Options{Dialect: "sqlite", Schema: "main", DesiredHCL: "schema \"main\" {}\n" + first + "\n" + second})
		Expect(err).NotTo(HaveOccurred())
		b, err := Build(ctx, db, Options{Dialect: "sqlite", Schema: "main", DesiredHCL: "schema \"main\" {}\n" + second + "\n" + first})
		Expect(err).NotTo(HaveOccurred())
		Expect(a.Hash).To(Equal(b.Hash))
	})
	It("previews without writing, applies the exact saved plan and recognizes a repeat", func(ctx SpecContext) {
		artifact, err := Build(ctx, db, Options{Dialect: "sqlite", Schema: "main", DesiredHCL: itemsHCL})
		Expect(err).NotTo(HaveOccurred())
		var count int
		Expect(db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE type = 'table'").Scan(&count)).To(Succeed())
		Expect(count).To(BeZero())
		result, err := Apply(ctx, db, Request{Artifact: artifact, Hash: artifact.Hash})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Outcome).To(Equal(Applied))
		Expect(result.Steps).To(Equal(len(artifact.Steps)))
		result, err = Apply(ctx, db, Request{Artifact: artifact, Hash: artifact.Hash})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Outcome).To(Equal(AlreadyApplied))
	})
	It("rejects a changed hash before touching the database", func(ctx SpecContext) {
		artifact, err := Build(ctx, db, Options{Dialect: "sqlite", Schema: "main", DesiredHCL: itemsHCL})
		Expect(err).NotTo(HaveOccurred())
		artifact.Steps[0].SQL = "DROP TABLE unrelated"
		_, err = Apply(ctx, nil, Request{Artifact: artifact, Hash: artifact.Hash})
		Expect(err).To(MatchError(ContainSubstring("hash")))
	})
	It("rejects schema drift under the write lock", func(ctx SpecContext) {
		artifact, err := Build(ctx, db, Options{Dialect: "sqlite", Schema: "main", DesiredHCL: itemsHCL})
		Expect(err).NotTo(HaveOccurred())
		_, err = db.Exec("CREATE TABLE intervening (id integer)")
		Expect(err).NotTo(HaveOccurred())
		_, err = Apply(ctx, db, Request{Artifact: artifact, Hash: artifact.Hash})
		Expect(err).To(MatchError(ContainSubstring("drift")))
	})
	It("requires each destructive step acknowledgement and preserves rows through a rebuild", func(ctx SpecContext) {
		_, err := db.Exec("CREATE TABLE items (id integer, label text, omitted text); INSERT INTO items VALUES (1, 'retained', 'removed')")
		Expect(err).NotTo(HaveOccurred())
		artifact, err := Build(ctx, db, Options{Dialect: "sqlite", Schema: "main", DesiredHCL: itemsHCL})
		Expect(err).NotTo(HaveOccurred())
		Expect(artifact.Acknowledgements()).NotTo(BeEmpty())
		_, err = Apply(ctx, db, Request{Artifact: artifact, Hash: artifact.Hash})
		Expect(err).To(MatchError(ContainSubstring("acknowledgement")))
		result, err := Apply(ctx, db, Request{Artifact: artifact, Hash: artifact.Hash, Acknowledgements: artifact.Acknowledgements()})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Outcome).To(Equal(Applied))
		var label string
		Expect(db.QueryRow("SELECT label FROM items WHERE id = 1").Scan(&label)).To(Succeed())
		Expect(label).To(Equal("retained"))
	})
	It("fails closed on unmanaged triggers", func(ctx SpecContext) {
		_, err := db.Exec("CREATE TABLE items (id integer); CREATE TRIGGER items_audit AFTER INSERT ON items BEGIN SELECT 1; END")
		Expect(err).NotTo(HaveOccurred())
		_, err = Build(ctx, db, Options{Dialect: "sqlite", Schema: "main", DesiredHCL: itemsHCL})
		Expect(err).To(MatchError(ContainSubstring("unsupported")))
	})
	It("rolls back a failed copy and restores connection pragmas", func(ctx SpecContext) {
		db.SetMaxOpenConns(1)
		_, err := db.Exec("PRAGMA foreign_keys = on; PRAGMA busy_timeout = 321; CREATE TABLE items (id integer, label text); INSERT INTO items VALUES (1, NULL)")
		Expect(err).NotTo(HaveOccurred())
		desired := `schema "main" {}
table "items" {
 schema = schema.main
 column "id" { type = integer }
 column "label" {
   type = text
   null = false
 }
}`
		artifact, err := Build(ctx, db, Options{Dialect: "sqlite", Schema: "main", DesiredHCL: desired})
		Expect(err).NotTo(HaveOccurred())
		_, err = Apply(ctx, db, Request{Artifact: artifact, Hash: artifact.Hash, Acknowledgements: artifact.Acknowledgements()})
		Expect(err).To(HaveOccurred())
		var foreignKeys, busyTimeout, count int
		Expect(db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys)).To(Succeed())
		Expect(db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout)).To(Succeed())
		Expect([]int{foreignKeys, busyTimeout}).To(Equal([]int{1, 321}))
		Expect(db.QueryRow("SELECT count(*) FROM items WHERE label IS NULL").Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))
	})
})
