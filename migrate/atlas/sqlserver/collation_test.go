package sqlserver_test

import (
	"ariga.io/atlas/sql/schema"
	planner "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL Server collation syntax", func() {
	It("writes collation literals in CREATE and ALTER COLUMN without identifier quoting", func() {
		column := schema.NewStringColumn("label", "nvarchar").SetType(&schema.StringType{T: "nvarchar", Size: 100}).SetCollation("SQL_Latin1_General_CP1_CI_AS")
		table := schema.NewTable("orders").AddColumns(column)
		schema.New("dbo").AddTables(table)
		Expect(planCmds(&schema.AddTable{T: table})).To(Equal([]string{"CREATE TABLE [dbo].[orders] ([label] nvarchar(100) COLLATE SQL_Latin1_General_CP1_CI_AS NOT NULL)"}))
		from := schema.NewStringColumn("label", "nvarchar").SetType(&schema.StringType{T: "nvarchar", Size: 20}).SetCollation("SQL_Latin1_General_CP1_CI_AS")
		Expect(planCmds(modify(table, &schema.ModifyColumn{From: from, To: column, Change: schema.ChangeType}))).To(Equal([]string{"ALTER TABLE [dbo].[orders] ALTER COLUMN [label] nvarchar(100) COLLATE SQL_Latin1_General_CP1_CI_AS NOT NULL"}))
	})

	It("rejects text that cannot be emitted as a collation literal", func() {
		for _, name := range []string{"", "Latin1; SELECT 1", "[Latin1_General_CI_AS]", "Latin1--comment", "Latin1 General"} {
			column := schema.NewStringColumn("label", "nvarchar").SetType(&schema.StringType{T: "nvarchar", Size: 100}).SetCollation(name)
			table := schema.NewTable("orders").AddColumns(column)
			schema.New("dbo").AddTables(table)
			_, err := planner.DefaultPlan.PlanChanges(GinkgoT().Context(), "invalid", []schema.Change{&schema.AddTable{T: table}})
			Expect(err).To(MatchError(ContainSubstring("invalid collation literal")))
		}
	})

	It("omits COLLATE when a text column becomes an integer", func() {
		from := schema.NewStringColumn("label", "nvarchar").SetType(&schema.StringType{T: "nvarchar", Size: 20}).SetCollation("SQL_Latin1_General_CP1_CI_AS")
		to := schema.NewIntColumn("label", "int")
		table := schema.NewTable("orders").AddColumns(to)
		schema.New("dbo").AddTables(table)
		Expect(planCmds(modify(table, &schema.ModifyColumn{From: from, To: to, Change: schema.ChangeType | schema.ChangeAttr}))).To(Equal([]string{"ALTER TABLE [dbo].[orders] ALTER COLUMN [label] int NOT NULL"}))
	})
})
