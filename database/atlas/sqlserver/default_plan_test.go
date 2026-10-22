package sqlserver

import (
	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ migrate.PlanApplier = DefaultPlan

var _ = Describe("connectionless default planner", func() {
	It("plans schema changes and refuses to execute them without a connection", func(ctx SpecContext) {
		table := schema.NewTable("items").SetSchema(schema.New("dbo")).AddColumns(schema.NewIntColumn("id", "int"))
		changes := []schema.Change{&schema.AddTable{T: table}}
		plan, err := DefaultPlan.PlanChanges(ctx, "example", changes)
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Changes).To(ContainElement(HaveField("Cmd", ContainSubstring("CREATE TABLE [dbo].[items]"))))
		Expect(DefaultPlan.ApplyChanges(ctx, changes)).To(MatchError(ContainSubstring("cannot execute statements without a database connection")))
	})
})
