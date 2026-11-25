package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/sqlglot-go/database/migrate/dialect"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	_ "modernc.org/sqlite"
)

func TestWorkflow(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Migration workflow") }

const itemsHCL = "schema \"main\" {}\ntable \"items\" {\n schema = schema.main\n column \"id\" { type = integer }\n}"

func openSQLite(path string) *sql.DB {
	GinkgoHelper()
	db, err := sql.Open("sqlite", path)
	Expect(err).NotTo(HaveOccurred())
	Expect(db.Ping()).To(Succeed())
	DeferCleanup(db.Close)
	return db
}

var _ = Describe("content-backed migration workflow", func() {
	var db *sql.DB
	BeforeEach(func() { db = openSQLite(filepath.Join(GinkgoT().TempDir(), "target.db")) })

	It("plans pasted HCL and applies the exact downloaded artifact idempotently", func() {
		ctx := context.Background()
		plan, err := Plan(ctx, db, Options{Dialect: "sqlite", Schema: "main", Source: Source{Kind: "hcl", Content: itemsHCL}})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Applicable).To(BeTrue())
		Expect(plan.SQL).To(ContainSubstring("CREATE TABLE"))
		var original engine.Artifact
		Expect(json.Unmarshal(plan.Artifact, &original)).To(Succeed())
		Expect(plan.Hash).To(Equal(original.Hash))
		Expect(plan.Original).To(MatchJSON(plan.Artifact), "a plan file written from a pasted source is the bare artifact")
		request := ApplyOptions{Dialect: "sqlite", Schema: "main", Artifact: plan.Artifact, Acks: plan.Acks, LockTimeout: time.Second}
		value, err := Apply(ctx, db, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(value).To(Equal(ApplyResult{Hash: plan.Hash, Outcome: engine.Applied, Steps: len(plan.Steps), Units: []engine.UnitResult{{ID: original.Units[0].ID, Outcome: engine.Applied, Statements: 1}}}))
		value, err = Apply(ctx, db, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(value).To(Equal(ApplyResult{Hash: plan.Hash, Outcome: engine.AlreadyApplied, Units: []engine.UnitResult{{ID: original.Units[0].ID, Outcome: engine.AlreadyApplied}}}))
		request.Artifact = json.RawMessage(strings.Replace(string(plan.Artifact), "CREATE TABLE", "CREATE TABLE IF NOT EXISTS", 1))
		_, err = Apply(ctx, db, request)
		Expect(err).To(MatchError(ContainSubstring("hash mismatch")), "an edited artifact fails its own integrity hash")
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

	It("plans toward an inspected snapshot and refuses a snapshot that is not for the target", func() {
		ctx := context.Background()
		reference := openSQLite(filepath.Join(GinkgoT().TempDir(), "reference.db"))
		_, err := reference.Exec("CREATE TABLE items (id integer NOT NULL, label text)")
		Expect(err).NotTo(HaveOccurred())
		snapshot, err := dialect.Inspect(ctx, reference, "sqlite", []string{"main"})
		Expect(err).NotTo(HaveOccurred())
		content, err := json.Marshal(snapshot)
		Expect(err).NotTo(HaveOccurred())
		plan, err := Plan(ctx, db, Options{Dialect: "sqlite", Schema: "main", Source: Source{Kind: "snapshot", Content: string(content)}})
		Expect(err).NotTo(HaveOccurred())
		Expect(changeIDs(plan)).To(Equal([]string{changeID("add", "table", "items", "")}))
		Expect(plan.Scope.Mode).To(Equal(engine.ScopeComplete))

		other := snapshot
		other.Dialect = "postgres"
		wrong, err := json.Marshal(other)
		Expect(err).NotTo(HaveOccurred())
		_, err = Plan(ctx, db, Options{Dialect: "sqlite", Schema: "main", Source: Source{Kind: "snapshot", Content: string(wrong)}})
		Expect(err).To(MatchError(ContainSubstring("the desired snapshot is for postgres, not the target's sqlite")))
		_, err = Plan(ctx, db, Options{Dialect: "sqlite", Schema: "main", Source: Source{Kind: "snapshot", Content: `{"dialect":"sqlite","schema":"main","hcl":"schema \"main\" {}"}`}})
		Expect(err).To(MatchError(ContainSubstring("not one that `sqlglot schema inspect --snapshot` writes")))
	})

	It("refuses desired HCL that declares another schema than the target's", func() {
		_, err := Plan(context.Background(), db, Options{Dialect: "sqlite", Schema: "main", Source: Source{Kind: "hcl", Content: strings.ReplaceAll(itemsHCL, "main", "public")}})
		Expect(err).To(MatchError(ContainSubstring("desired object public.items is outside the schemas [main]")))
	})

	It("shows an advisory preview's plan without making it applicable, and refuses to apply it", func() {
		ctx := context.Background()
		plan, err := Plan(ctx, db, Options{Dialect: "sqlite", Schema: "main", Source: Source{Kind: "hcl", Content: itemsHCL}})
		Expect(err).NotTo(HaveOccurred())
		preview, err := json.Marshal(map[string]any{
			"artifact": plan.Artifact, "warnings": []string{"pre-script seed.sql is ddl"}, "advisory": []string{"SQL scripts require phased reviewed artifacts"},
		})
		Expect(err).NotTo(HaveOccurred())

		reviewed, err := Review(preview, "sqlite", "main")
		Expect(err).NotTo(HaveOccurred())
		Expect(reviewed.Applicable).To(BeFalse())
		Expect(reviewed.Steps).To(HaveLen(1))
		Expect(reviewed.Warnings).To(Equal([]string{"pre-script seed.sql is ddl", "SQL scripts require phased reviewed artifacts"}))
		Expect(reviewed.Advisory).To(Equal([]string{"SQL scripts require phased reviewed artifacts"}))
		Expect(reviewed.Artifact).To(MatchJSON(plan.Artifact))
		Expect(reviewed.Original).To(MatchJSON(preview))

		_, err = Apply(ctx, db, ApplyOptions{Dialect: "sqlite", Schema: "main", Artifact: preview})
		Expect(err).To(MatchError(ContainSubstring("advisory preview cannot be applied")))
	})
})
