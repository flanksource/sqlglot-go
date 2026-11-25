package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/flanksource/sqlglot-go/database/migrate/dialect"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Characterization of the reviewed SQLite path, through the workflow: plan,
// artifact, apply and re-plan. These are the scenarios the previous reviewed
// path was pinned with; only the acknowledgement identity differs, a change ID
// now instead of the hash of a statement's position and text.

const characterizedHCL = `schema "main" {}
table "items" {
  schema = schema.main
  column "id" { type = integer }
  column "label" { type = text }
}`

func changeID(action, kind, table, member string) string {
	id, err := json.Marshal([]string{action, kind, "main", table, member})
	Expect(err).NotTo(HaveOccurred())
	return string(id)
}

func planCharacterized(ctx context.Context, db *sql.DB) PlanResult {
	GinkgoHelper()
	plan, err := Plan(ctx, db, Options{Dialect: "sqlite", Schema: "main", Source: Source{Kind: "hcl", Content: characterizedHCL}})
	Expect(err).NotTo(HaveOccurred())
	return plan
}

func changeIDs(plan PlanResult) []string {
	ids := []string{}
	for _, c := range plan.Changes {
		ids = append(ids, c.ID)
	}
	return ids
}

func applyPlan(ctx context.Context, db *sql.DB, plan PlanResult, acks ...string) (any, error) {
	return Apply(ctx, db, ApplyOptions{Dialect: "sqlite", Schema: "main", Artifact: plan.Artifact, Acks: acks})
}

var _ = Describe("SQLite workflow characterization", func() {
	var db *sql.DB
	BeforeEach(func() { db = openSQLite(filepath.Join(GinkgoT().TempDir(), "characterization.db")) })

	It("plans a create as one non-destructive step, applies it and re-plans to nothing", func(ctx SpecContext) {
		plan := planCharacterized(ctx, db)
		addItems := changeID("add", "table", "items", "")
		Expect(changeIDs(plan)).To(Equal([]string{addItems}))
		Expect(plan.Steps).To(HaveLen(1))
		Expect(plan.Steps[0]).To(Equal(Step{
			ID: plan.Steps[0].ID, SQL: "CREATE TABLE `items` (`id` integer NOT NULL, `label` text NOT NULL)", Destructive: false,
			Change: addItems, Unit: plan.Units[0].ID, Phase: "create",
		}))
		Expect(plan.Acks).To(BeEmpty())

		result, err := applyPlan(ctx, db, plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(HaveField("Outcome", engine.Applied))
		Expect(result).To(HaveField("Steps", 1))

		replanned := planCharacterized(ctx, db)
		Expect(replanned.Changes).To(BeEmpty())
		Expect(replanned.Steps).To(BeEmpty())
		var first, again engine.Artifact
		Expect(json.Unmarshal(plan.Artifact, &first)).To(Succeed())
		Expect(json.Unmarshal(replanned.Artifact, &again)).To(Succeed())
		Expect(again.Baseline).To(Equal(first.Desired))
	})

	It("marks a column drop destructive, refuses a wrong acknowledgement set and applies the exact one", func(ctx SpecContext) {
		_, err := db.Exec("CREATE TABLE items (id integer NOT NULL, label text NOT NULL, omitted text)")
		Expect(err).NotTo(HaveOccurred())
		plan := planCharacterized(ctx, db)
		drop := changeID("drop", "column", "items", "omitted")
		Expect(changeIDs(plan)).To(Equal([]string{drop}))
		sqls := []string{}
		for _, s := range plan.Steps {
			sqls = append(sqls, s.SQL)
			Expect(s.Destructive).To(BeTrue(), "every statement of the rebuild implements the destructive change")
		}
		Expect(sqls).To(Equal([]string{
			"CREATE TABLE `new_items` (`id` integer NOT NULL, `label` text NOT NULL)",
			"INSERT INTO `new_items` (`id`, `label`) SELECT `id`, `label` FROM `items`",
			"DROP TABLE `items`",
			"ALTER TABLE `new_items` RENAME TO `items`",
		}))
		Expect(plan.Acks).To(Equal([]string{drop}))

		_, err = applyPlan(ctx, db, plan)
		Expect(err).To(MatchError(ContainSubstring("requires acknowledgements")))
		_, err = applyPlan(ctx, db, plan, drop, "unknown")
		Expect(err).To(MatchError(ContainSubstring("unknown or duplicate destructive acknowledgement")))
		result, err := applyPlan(ctx, db, plan, drop)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(HaveField("Outcome", engine.Applied))
		Expect(planCharacterized(ctx, db).Changes).To(BeEmpty())
	})

	It("keeps an undeclared column, plans nothing for it and leaves the table untouched, in declared scope, when the table is declared partial", func(ctx SpecContext) {
		_, err := db.Exec("CREATE TABLE items (id integer NOT NULL, label text NOT NULL, omitted text); INSERT INTO items VALUES (1, 'kept', 'dba value')")
		Expect(err).NotTo(HaveOccurred())
		desired, err := dialect.DesiredFromHCL("sqlite", strings.Replace(characterizedHCL, "schema = schema.main\n", "schema = schema.main\n  partial = true\n", 1))
		Expect(err).NotTo(HaveOccurred())
		Expect(desired.Tables[0].Partial).To(BeTrue())
		artifact, err := engine.PlanLive(ctx, db, engine.PlanLiveRequest{Dialect: "sqlite", Scope: engine.Scope{Mode: engine.ScopeDeclared, Schemas: []string{"main"}}, Desired: desired})
		Expect(err).NotTo(HaveOccurred())
		Expect(artifact.Changes).To(BeEmpty())
		Expect(artifact.Preserved).To(HaveLen(1))
		data, err := json.Marshal(artifact)
		Expect(err).NotTo(HaveOccurred())

		plan, err := Review(data, "sqlite", "main")
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Preserved).To(HaveLen(1))
		Expect(plan.Scope.Mode).To(Equal(engine.ScopeDeclared))
		result, err := applyPlan(ctx, db, plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(HaveField("Outcome", engine.AlreadyApplied))
		var omitted string
		Expect(db.QueryRow("SELECT omitted FROM items WHERE id = 1").Scan(&omitted)).To(Succeed())
		Expect(omitted).To(Equal("dba value"))
	})

	It("refuses an artifact once the planned table changed after the preview and changes nothing", func(ctx SpecContext) {
		_, err := db.Exec("CREATE TABLE items (id integer NOT NULL)")
		Expect(err).NotTo(HaveOccurred())
		plan := planCharacterized(ctx, db)
		_, err = db.Exec("ALTER TABLE items ADD COLUMN intervening text")
		Expect(err).NotTo(HaveOccurred())
		_, err = applyPlan(ctx, db, plan)
		var drift *engine.DriftError
		Expect(errors.As(err, &drift)).To(BeTrue(), "got %v", err)
		Expect(err).To(MatchError(ContainSubstring("drifted from the artifact baseline")))
		var columns int
		Expect(db.QueryRow("SELECT count(*) FROM pragma_table_info('items')").Scan(&columns)).To(Succeed())
		Expect(columns).To(Equal(2))
	})
})
