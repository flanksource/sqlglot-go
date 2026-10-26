package compare_test

import (
	"github.com/flanksource/sqlglot-go/database/compare"
	"github.com/flanksource/sqlglot-go/migrate"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = DescribeTable("Labelling a side in task output without credentials",
	func(dsn, expected string) { Expect(compare.Label(dsn)).To(Equal(expected)) },
	Entry("named connection", "connection://acme/prod", "acme/prod"),
	Entry("URL DSN drops user, password and query", "sqlserver://sa:s3cret@db.example:1433/main?database=payroll", "db.example:1433/main"),
	Entry("SQLite file", "sqlite:///var/data/side.db", "side.db"),
	Entry("keyword DSN has no safe label", "server=db.example;user id=sa;password=s3cret", ""),
)

var _ = Describe("SQL Server changed objects", func() {
	It("finds module definitions inspected with upper-case kinds for planner changes with lower-case kinds", func() {
		var (
			proc    = migrate.Name{Schema: "dbo", Name: "payroll_file"}
			view    = migrate.Name{Schema: "dbo", Name: "open_orders"}
			fn      = migrate.Name{Schema: "dbo", Name: "net_pay"}
			procV1  = "CREATE PROCEDURE dbo.payroll_file AS SELECT 1"
			procV2  = "CREATE PROCEDURE dbo.payroll_file AS SELECT 2"
			viewSQL = "CREATE VIEW dbo.open_orders AS SELECT 1 AS id"
			fnSQL   = "CREATE FUNCTION dbo.net_pay() RETURNS int AS BEGIN RETURN 1 END"
		)
		current := migrate.Schema{Definitions: []migrate.Definition{
			{Name: proc, Kind: "PROCEDURE", SQL: procV1},
			{Name: view, Kind: "VIEW", SQL: viewSQL},
		}}
		desired := migrate.Schema{Definitions: []migrate.Definition{
			{Name: proc, Kind: "PROCEDURE", SQL: procV2},
			{Name: fn, Kind: "FUNCTION", SQL: fnSQL},
		}}
		objects, err := compare.SQLServerObjects(current, desired, []migrate.Change{
			{ID: "modify-proc", Kind: "procedure", Object: proc},
			{ID: "add-fn", Kind: "function", Object: fn},
			{ID: "drop-view", Kind: "view", Object: view},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(objects).To(Equal([]compare.Object{
			{Kind: "procedure", Name: proc, Status: "changed", Before: procV1, After: procV2, Language: "sql", Changes: []string{"modify-proc"}},
			{Kind: "function", Name: fn, Status: "added", After: fnSQL, Language: "sql", Changes: []string{"add-fn"}},
			{Kind: "view", Name: view, Status: "deleted", Before: viewSQL, Language: "sql", Changes: []string{"drop-view"}},
		}))
	})
})
