package sqlserver_test

import (
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	"github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("The v1 offline planning API", func() {
	var (
		withNote = schemaOf(charTable(charOrders, charColumn("id", "int", false), charColumn("note", "nvarchar(50)", true)))
		addNote  = charID("add", "column", charOrders, "note")
		addOrder = charID("add", "table", charCustomers, "")
	)
	stepSQL := func(p migrate.Plan) []string {
		out := []string{}
		for _, s := range p.Steps {
			out = append(out, s.SQL)
		}
		return out
	}

	It("plans the engine's ordered statements as steps", func() {
		plan, err := sqlserver.Plan(migrate.Request{Desired: keyedOrders()})
		Expect(err).NotTo(HaveOccurred())
		Expect(stepSQL(plan)).To(Equal(statementsOf(mustPlanBetween(migrate.Schema{}, keyedOrders()))))
		Expect(plan.Steps[0].Change.ID).To(Equal(addOrder))
	})

	It("lists the unselected changes beside the selected steps", func() {
		desired := withNote
		desired.Tables = append(desired.Tables, charTable(charCustomers, charColumn("id", "int", false)))
		current := schemaOf(charTable(charOrders, charColumn("id", "int", false)))
		plan, err := sqlserver.Plan(migrate.Request{Current: current, Desired: desired, Selection: []string{addNote}})
		Expect(err).NotTo(HaveOccurred())
		Expect(stepSQL(plan)).To(Equal([]string{"ALTER TABLE [dbo].[orders] ADD [note] nvarchar(50) NULL"}))
		Expect(changeIDs(plan.Changes)).To(ConsistOf(addNote, addOrder))
	})

	It("binds an artifact whose hash ignores selection and snapshot order", func() {
		desired := withNote
		desired.Tables = append(desired.Tables, charTable(charCustomers, charColumn("id", "int", false)))
		reordered := migrate.Schema{Tables: []migrate.Table{desired.Tables[1], desired.Tables[0]}}
		a, err := sqlserver.BuildArtifact(migrate.ArtifactRequest{Schemas: []string{"dbo"}, Request: migrate.Request{Desired: desired, Selection: []string{addOrder, charID("add", "table", charOrders, "")}}})
		Expect(err).NotTo(HaveOccurred())
		b, err := sqlserver.BuildArtifact(migrate.ArtifactRequest{Schemas: []string{"dbo"}, Request: migrate.Request{Desired: reordered, Selection: []string{charID("add", "table", charOrders, ""), addOrder}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(b.Hash).To(Equal(a.Hash))
		Expect(sqlserver.ValidateArtifact(a)).To(Succeed())
		a.Plan.Steps[0].SQL += " -- edited"
		Expect(sqlserver.ValidateArtifact(a)).To(MatchError(ContainSubstring("hash mismatch")))
	})

	It("refuses an artifact snapshot outside its schema scope", func() {
		_, err := sqlserver.BuildArtifact(migrate.ArtifactRequest{Schemas: []string{"sales"}, Request: migrate.Request{Desired: withNote}})
		Expect(err).To(MatchError(ContainSubstring("dbo.orders is outside artifact schema scope")))
	})

	It("refuses a dialect that is not the one the request names", func() {
		_, err := engine.Plan(engine.PlanRequest{Dialect: "postgres", Using: sqlserver.Planner{}, Scope: engine.Scope{Mode: engine.ScopeComplete}})
		Expect(err).To(MatchError(ContainSubstring(`names dialect "postgres" but uses "sqlserver"`)))
	})
})

func changeIDs(changes []migrate.Change) []string {
	ids := []string{}
	for _, c := range changes {
		ids = append(ids, c.ID)
	}
	return ids
}
