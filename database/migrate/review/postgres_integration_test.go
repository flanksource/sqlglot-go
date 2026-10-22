//go:build integration

package review

import (
	"database/sql"
	"errors"
	"fmt"
	_ "github.com/lib/pq"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"net/url"
	"os"
	"time"
)

var _ = Describe("PostgreSQL reviewed artifacts", func() {
	var db *sql.DB
	BeforeEach(func(ctx SpecContext) {
		dsn := os.Getenv("SQLGLOT_TEST_POSTGRES_URL")
		Expect(dsn).NotTo(BeEmpty(), "integration tests require a disposable PostgreSQL server")
		admin, err := sql.Open("postgres", dsn)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(admin.Close)
		name := fmt.Sprintf("sqlglot_review_%d", time.Now().UnixNano())
		_, err = admin.ExecContext(ctx, "CREATE DATABASE "+name)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _, e := admin.Exec("DROP DATABASE " + name); Expect(e).NotTo(HaveOccurred()) })
		u, err := url.Parse(dsn)
		Expect(err).NotTo(HaveOccurred())
		u.Path = "/" + name
		db, err = sql.Open("postgres", u.String())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
	})
	It("recognizes backend-rendered parentheses in named checks", func(ctx SpecContext) {
		desired := `schema "public" {}
table "items" {
 schema = schema.public
 column "id" { type = integer }
 check "positive_id" { expr = "id > 0" }
}`
		artifact, err := Build(ctx, db, Options{Dialect: "postgres", Schema: "public", DesiredHCL: desired})
		Expect(err).NotTo(HaveOccurred())
		result, err := Apply(ctx, db, Request{Artifact: artifact, Hash: artifact.Hash})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Outcome).To(Equal(Applied))
		result, err = Apply(ctx, db, Request{Artifact: artifact, Hash: artifact.Hash})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Outcome).To(Equal(AlreadyApplied))
	})
	It("plans read-only, applies exact SQL and recognizes the resulting schema", func(ctx SpecContext) {
		desired := `schema "public" {}
table "items" {
 schema = schema.public
 column "id" { type = integer }
 column "label" { type = text }
}`
		artifact, err := Build(ctx, db, Options{Dialect: "postgres", Schema: "public", DesiredHCL: desired})
		Expect(err).NotTo(HaveOccurred())
		var tables int
		Expect(db.QueryRow("SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'").Scan(&tables)).To(Succeed())
		Expect(tables).To(BeZero())
		result, err := Apply(ctx, db, Request{Artifact: artifact, Hash: artifact.Hash})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Outcome).To(Equal(Applied))
		result, err = Apply(ctx, db, Request{Artifact: artifact, Hash: artifact.Hash})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Outcome).To(Equal(AlreadyApplied))
	})
	It("preserves enum snapshots through reviewed creation", func(ctx SpecContext) {
		desired := `schema "public" {}
enum "state" {
 schema = schema.public
 values = ["new", "done"]
}
table "items" {
 schema = schema.public
 column "state" { type = enum.state }
}`
		artifact, err := Build(ctx, db, Options{Dialect: "postgres", Schema: "public", DesiredHCL: desired})
		Expect(err).NotTo(HaveOccurred())
		result, err := Apply(ctx, db, Request{Artifact: artifact, Hash: artifact.Hash})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Outcome).To(Equal(Applied))
	})
	It("rolls back failed DDL and rejects drift", func(ctx SpecContext) {
		_, err := db.Exec("CREATE TABLE items (label text); INSERT INTO items VALUES (NULL)")
		Expect(err).NotTo(HaveOccurred())
		desired := `schema "public" {}
table "items" {
 schema = schema.public
 column "label" { type = text }
}`
		artifact, err := Build(ctx, db, Options{Dialect: "postgres", Schema: "public", DesiredHCL: desired})
		Expect(err).NotTo(HaveOccurred())
		_, err = Apply(ctx, db, Request{Artifact: artifact, Hash: artifact.Hash, Acknowledgements: artifact.Acknowledgements()})
		Expect(err).To(HaveOccurred())
		var count int
		Expect(db.QueryRow("SELECT count(*) FROM items WHERE label IS NULL").Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))
		_, err = db.Exec("ALTER TABLE items ADD COLUMN intervening text")
		Expect(err).NotTo(HaveOccurred())
		_, err = Apply(ctx, db, Request{Artifact: artifact, Hash: artifact.Hash, Acknowledgements: artifact.Acknowledgements()})
		Expect(err).To(MatchError(ContainSubstring("drift")))
	})
	It("bounds waiting for another migration transaction", func(ctx SpecContext) {
		snapshot, err := Inspect(ctx, db, "postgres", "public")
		Expect(err).NotTo(HaveOccurred())
		artifact, err := Build(ctx, db, Options{Dialect: "postgres", Schema: "public", DesiredHCL: snapshot.HCL})
		Expect(err).NotTo(HaveOccurred())
		tx, err := db.BeginTx(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			if err := tx.Rollback(); !errors.Is(err, sql.ErrTxDone) {
				Expect(err).NotTo(HaveOccurred())
			}
		})
		_, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(7561364747649)")
		Expect(err).NotTo(HaveOccurred())
		_, err = Apply(ctx, db, Request{Artifact: artifact, Hash: artifact.Hash, LockTimeout: 25 * time.Millisecond})
		Expect(err).To(MatchError(ContainSubstring("acquire migration lock")))
		Expect(tx.Rollback()).To(Succeed())
	})
})
