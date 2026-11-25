//go:build integration

package sqlserver

import (
	"errors"
	"strings"

	"ariga.io/atlas/sql/schema"
	atlashcl "github.com/flanksource/sqlglot-go/database/atlas/sqlserver"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The same HCL the bundle path's characterization uses. The bundle path
// (database/migrate) plans through this dialect, so its own specs cover the
// end state; these cover the HCL model against the live catalog.
const parityHCL = `schema "SCHEMA" {}

table "acme_orders" {
  schema = schema.SCHEMA
  column "id" {
    type = int
    identity {
      seed      = 1
      increment = 1
    }
  }
  column "status" {
    type    = nvarchar(20)
    default = "open"
  }
  column "qty" {
    type    = int
    default = 0
  }
  EXTRA_COLUMNS
  primary_key {
    columns = [column.id]
  }
}
`

const parityNote = `column "note" {
    type = nvarchar(50)
    null = true
  }`

func parityDesired(schemaName, extraColumns string) migrate.Schema {
	GinkgoHelper()
	text := strings.NewReplacer("SCHEMA", schemaName, "EXTRA_COLUMNS", extraColumns).Replace(parityHCL)
	realm := &schema.Realm{}
	Expect(atlashcl.EvalHCLBytes([]byte(text), realm, nil)).To(Succeed())
	desired, err := planner.FromAtlas(realm)
	Expect(err).NotTo(HaveOccurred())
	return desired
}

var _ = Describe("the HCL model against the live schema", Ordered, Label("integration"), func() {
	scope := declared("hclnew", "acme_orders")

	It("creates the declared table, and a re-plan has no changes and no units", func(ctx SpecContext) {
		mustExec(ctx, db, "CREATE SCHEMA hclnew")
		artifact, err := planLive(ctx, db, scope, parityDesired("hclnew", ""))
		Expect(err).NotTo(HaveOccurred())
		Expect(changeIDs(artifact, "add")).To(HaveLen(1))
		result, err := applyArtifact(ctx, db, artifact)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Outcome).To(Equal(engine.Applied))

		again, err := planLive(ctx, db, scope, parityDesired("hclnew", ""))
		Expect(err).NotTo(HaveOccurred())
		Expect(again.Changes).To(BeEmpty())
		Expect(again.Units).To(BeEmpty())
	})

	It("adds a declared column, keeping a DBA's index and an undeclared column", func(ctx SpecContext) {
		mustExec(ctx, db, "CREATE INDEX IX_dba_tuning ON hclnew.acme_orders (status)", "ALTER TABLE hclnew.acme_orders ADD dba_note nvarchar(20) NULL")
		desired := parityDesired("hclnew", parityNote)
		artifact, err := planLive(ctx, db, scope, desired)
		Expect(err).NotTo(HaveOccurred())
		Expect(artifact.Changes).To(HaveLen(1))
		Expect(artifact.Changes[0].ID).To(Equal(`["add","column","hclnew","acme_orders","note"]`))
		for _, c := range artifact.Changes {
			Expect(c.Action).NotTo(Equal("drop"))
		}
		_, err = applyArtifact(ctx, db, artifact)
		Expect(err).NotTo(HaveOccurred())
		Expect(scalar[int](ctx, db, "SELECT COUNT(*) FROM sys.indexes WHERE object_id = OBJECT_ID('hclnew.acme_orders') AND name = 'IX_dba_tuning'")).To(Equal(1))
		Expect(scalar[int](ctx, db, "SELECT COUNT(*) FROM sys.columns WHERE object_id = OBJECT_ID('hclnew.acme_orders') AND name IN ('note', 'dba_note')")).To(Equal(2))

		again, err := planLive(ctx, db, scope, desired)
		Expect(err).NotTo(HaveOccurred())
		Expect(again.Changes).To(BeEmpty())
	})
})

var _ = Describe("a plan of independent units that stops midway", Ordered, Label("integration"), func() {
	scope := declared("res", "a_tbl", "b_tbl")
	var artifact engine.Artifact

	retypeAll := func(s migrate.Schema) migrate.Schema {
		out := cloneSchema(s)
		for _, t := range out.Tables {
			c := column(tableOf(out, t.Name), "g")
			c.Type, c.Collation = "uniqueidentifier", ""
		}
		return out
	}

	It("keeps the first unit committed when the second one's precondition fails", func(ctx SpecContext) {
		mustExec(ctx, db, "CREATE SCHEMA res",
			"CREATE TABLE res.a_tbl (id int NOT NULL PRIMARY KEY, g varchar(36) NOT NULL)",
			"CREATE TABLE res.b_tbl (id int NOT NULL PRIMARY KEY, g varchar(36) NOT NULL)",
			"CREATE INDEX IX_a_g ON res.a_tbl (g)", "CREATE INDEX IX_b_g ON res.b_tbl (g)",
			"INSERT INTO res.a_tbl VALUES (1, '0A1B2C3D-0000-4000-8000-00000000AAAA')",
			"INSERT INTO res.b_tbl VALUES (1, 'not-a-guid')")
		var err error
		artifact, err = planLive(ctx, db, scope, retypeAll(inspect(ctx, db, scope).Schema))
		Expect(err).NotTo(HaveOccurred())
		Expect(artifact.Units).To(HaveLen(2))

		result, err := applyArtifact(ctx, db, artifact)
		var failed *engine.PreconditionError
		Expect(err).To(HaveOccurred())
		Expect(errors.As(err, &failed)).To(BeTrue(), "%v", err)
		Expect(result.Units).To(HaveLen(2))
		Expect([]engine.Outcome{result.Units[0].Outcome, result.Units[1].Outcome}).To(Equal([]engine.Outcome{engine.Applied, engine.Failed}))
		Expect(scalar[string](ctx, db, "SELECT t.name FROM sys.columns c JOIN sys.types t ON t.user_type_id = c.user_type_id WHERE c.object_id = OBJECT_ID('res.a_tbl') AND c.name = 'g'")).To(Equal("uniqueidentifier"))
		Expect(scalar[string](ctx, db, "SELECT t.name FROM sys.columns c JOIN sys.types t ON t.user_type_id = c.user_type_id WHERE c.object_id = OBJECT_ID('res.b_tbl') AND c.name = 'g'")).To(Equal("varchar"))
	})

	It("reports the first unit applied and the second pending", func(ctx SpecContext) {
		states, err := engine.Check(ctx, db, artifact)
		Expect(err).NotTo(HaveOccurred())
		Expect([]engine.UnitState{states[artifact.Units[0].ID], states[artifact.Units[1].ID]}).To(Equal([]engine.UnitState{engine.UnitApplied, engine.UnitPending}))
	})

	It("applies only the second unit once the data is fixed", func(ctx SpecContext) {
		mustExec(ctx, db, "UPDATE res.b_tbl SET g = '0A1B2C3D-0000-4000-8000-00000000BBBB'")
		result, err := applyArtifact(ctx, db, artifact)
		Expect(err).NotTo(HaveOccurred())
		Expect([]engine.Outcome{result.Units[0].Outcome, result.Units[1].Outcome}).To(Equal([]engine.Outcome{engine.AlreadyApplied, engine.Applied}))
		Expect(result.Outcome).To(Equal(engine.Applied))
	})
})
