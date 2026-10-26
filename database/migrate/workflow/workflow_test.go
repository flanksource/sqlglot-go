package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/sqlglot-go/database/migrate/review"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	_ "modernc.org/sqlite"
)

func TestWorkflow(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Migration workflow") }

var _ = Describe("content-backed migration workflow", func() {
	It("displays SQL Server advisory preview plans without making them applicable", func() {
		data := []byte(`{"sqlServerPlan":{"steps":[{"change":{"id":"preview-only"},"sql":"SELECT 1"}],"changes":[]},"warnings":["SQL scripts require phased reviewed artifacts"]}`)
		plan, err := Review(data, "sqlserver", "dbo")
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Applicable).To(BeFalse())
		Expect(plan.Steps).To(HaveLen(1))
		Expect(plan.SQL).To(ContainSubstring("SELECT 1"))
		Expect(plan.Warnings).To(ContainElement("SQL scripts require phased reviewed artifacts"))
	})
	var db *sql.DB
	BeforeEach(func() {
		var err error
		db, err = sql.Open("sqlite", filepath.Join(GinkgoT().TempDir(), "target.db"))
		Expect(err).NotTo(HaveOccurred())
		Expect(db.Ping()).To(Succeed())
		DeferCleanup(db.Close)
	})
	It("plans pasted HCL and applies the exact downloaded artifact idempotently", func() {
		ctx := context.Background()
		plan, err := Plan(ctx, db, Options{Dialect: "sqlite", Schema: "main", Source: Source{Kind: "hcl", Content: "schema \"main\" {}\ntable \"items\" {\n schema = schema.main\n column \"id\" { type = integer }\n}"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Applicable).To(BeTrue())
		Expect(plan.SQL).To(ContainSubstring("CREATE TABLE"))
		var original review.Artifact
		Expect(json.Unmarshal(plan.Artifact, &original)).To(Succeed())
		Expect(plan.Hash).To(Equal(original.Hash))
		request := ApplyOptions{Dialect: "sqlite", Schema: "main", Artifact: plan.Artifact, Hash: plan.Hash, Acks: plan.Acks, LockTimeout: time.Second}
		value, err := Apply(ctx, db, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(value).To(Equal(review.Result{Hash: plan.Hash, Outcome: review.Applied, Steps: len(plan.Steps)}))
		value, err = Apply(ctx, db, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(value).To(Equal(review.Result{Hash: plan.Hash, Outcome: review.AlreadyApplied}))
		request.Hash = "tampered"
		_, err = Apply(ctx, db, request)
		Expect(err).To(MatchError(ContainSubstring("hash mismatch")))
	})
	It("produces identical plans from uploaded and on-disk bundle contents", func() {
		content := "schema \"public\" {}\ntable \"items\" {\n schema = schema.public\n column \"id\" { type = int }\n}"
		dir := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(dir, "schema.hcl"), []byte(content), 0600)).To(Succeed())
		local, err := Plan(context.Background(), db, Options{Dialect: "sqlite", Schema: "main", Bundle: os.DirFS(dir)})
		Expect(err).NotTo(HaveOccurred())
		uploaded, err := Plan(context.Background(), db, Options{Dialect: "sqlite", Schema: "main", Source: Source{Kind: "bundle", Files: []File{{Path: "schema.hcl", Content: content}}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(uploaded.Artifact).To(MatchJSON(local.Artifact))
		Expect(uploaded.Hash).To(Equal(local.Hash))
	})
	DescribeTable("rejects ambiguous bundle paths before planning", func(files []File) {
		_, err := Plan(context.Background(), db, Options{Dialect: "sqlite", Schema: "main", Source: Source{Kind: "bundle", Files: files}})
		Expect(err).To(MatchError(ContainSubstring("bundle path")))
	},
		Entry("parent traversal", []File{{Path: "../schema.hcl"}}),
		Entry("absolute paths", []File{{Path: "/schema.hcl"}}),
		Entry("duplicate paths", []File{{Path: "schema.hcl"}, {Path: "schema.hcl"}}),
	)
	It("requires destructive acknowledgements and refuses target drift", func() {
		ctx := context.Background()
		_, err := db.Exec("CREATE TABLE obsolete (id integer)")
		Expect(err).NotTo(HaveOccurred())
		plan, err := Plan(ctx, db, Options{Dialect: "sqlite", Schema: "main", Source: Source{Kind: "hcl", Content: "schema \"main\" {}"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Acks).NotTo(BeEmpty())
		request := ApplyOptions{Dialect: "sqlite", Schema: "main", Artifact: plan.Artifact, Hash: plan.Hash}
		_, err = Apply(ctx, db, request)
		Expect(err).To(HaveOccurred())
		request.Acks = plan.Acks
		_, err = db.Exec("CREATE TABLE unexpected (id integer)")
		Expect(err).NotTo(HaveOccurred())
		_, err = Apply(ctx, db, request)
		Expect(err).To(MatchError(ContainSubstring("drift")))
	})
	It("preserves advisory artifacts for review while refusing apply", func() {
		artifact := review.Artifact{Advisory: []string{"manual review required"}, Baseline: review.Snapshot{Dialect: "sqlite", Schema: "main"}}
		data, err := json.Marshal(artifact)
		Expect(err).NotTo(HaveOccurred())
		plan, err := Review(data, "sqlite", "main")
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Applicable).To(BeFalse())
		Expect(plan.Artifact).To(MatchJSON(data))
		Expect(plan.Warnings).To(ContainElement("manual review required"))
		_, err = Apply(context.Background(), db, ApplyOptions{Dialect: "sqlite", Schema: "main", Artifact: plan.Artifact, Hash: "unreviewed"})
		Expect(err).To(HaveOccurred())
	})
})
