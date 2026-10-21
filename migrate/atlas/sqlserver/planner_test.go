package sqlserver_test

import (
	"context"
	"testing"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPlanner(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SQL Server Migration Planner")
}

var _ = Describe("Extracted SQL Server planner", func() {
	It("creates a qualified table without opening a database", func() {
		table := schema.NewTable("orders").AddColumns(schema.NewIntColumn("id", "int"))
		schema.New("dbo").AddTables(table)
		plan, err := sqlserver.DefaultPlan.PlanChanges(context.Background(), "orders", []schema.Change{&schema.AddTable{T: table}})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Changes).To(HaveLen(1))
		Expect(plan.Changes[0].Cmd).To(Equal("CREATE TABLE [dbo].[orders] ([id] int NOT NULL)"))
	})

	It("distinguishes SQL Server real and Unicode text types", func() {
		for _, name := range []string{"real", "ntext"} {
			typ, err := sqlserver.ParseType(name)
			Expect(err).NotTo(HaveOccurred())
			text, err := sqlserver.FormatType(typ)
			Expect(err).NotTo(HaveOccurred())
			Expect(text).To(Equal(name))
		}
	})
})
