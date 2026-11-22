package scripts_test

import (
	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	"github.com/flanksource/sqlglot-go/sqlglot"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Script dependency analysis", func() {
	DescribeTable("recognizes executable function references",
		func(sql string, calls []string) {
			analysis := scripts.Analyze(scripts.AnalysisOptions{SQL: sql})
			tree, err := sqlglot.ParseScript(sql, "tsql")
			Expect(err).NotTo(HaveOccurred())
			Expect(analysis.Blocked).To(BeEmpty(), string(tree.Tree.DumpJSON()))
			Expect(analysis.Calls).To(Equal(calls))
		},
		Entry("qualified", "SELECT dbo.get_country_name()", []string{"dbo.get_country_name"}),
		Entry("brackets and nested expressions", "SELECT UPPER([dbo].[get_country_name]())", []string{"dbo.get_country_name"}),
		Entry("literal is not a call", "SELECT 'dbo.get_country_name()'", []string{}),
		Entry("constant dynamic SQL", "EXEC(N'SELECT dbo.get_country_name()')", []string{"dbo.get_country_name"}),
		Entry("procedure call", "EXEC dbo.refresh_data", []string{"dbo.refresh_data"}),
		Entry("module definition is deferred", "CREATE PROCEDURE dbo.example AS SELECT dbo.get_country_name()", []string{}),
	)
	DescribeTable("reports SQL known only at run time as dynamic, never as blocked",
		func(sql string) {
			analysis := scripts.Analyze(scripts.AnalysisOptions{SQL: sql})
			Expect(analysis.Blocked).To(BeEmpty())
			Expect(analysis.Dynamic).To(BeTrue())
		},
		Entry("EXEC of a variable", "EXEC(@sql)"),
		Entry("sp_executesql of a variable", "EXECUTE sp_executesql @sql;"),
		Entry("nested in constant dynamic SQL", "EXEC(N'EXEC(@sql)')"),
	)
	It("does not call constant dynamic SQL dynamic", func() {
		Expect(scripts.Analyze(scripts.AnalysisOptions{SQL: "EXEC(N'SELECT 1')"}).Dynamic).To(BeFalse())
	})
	It("still blocks SQL it cannot parse", func() {
		Expect(scripts.Analyze(scripts.AnalysisOptions{SQL: "BULK INSERT dbo.Orders FROM 'orders.csv'"}).Blocked).NotTo(BeEmpty())
	})
	It("inspects calls and volatility inside invoked stored modules", func() {
		analysis := scripts.Analyze(scripts.AnalysisOptions{SQL: "CREATE PROCEDURE dbo.example AS SELECT dbo.get_country_name(), GETDATE()", InvokedModule: true})
		Expect(analysis.Blocked).To(BeEmpty())
		Expect(analysis.Calls).To(Equal([]string{"dbo.get_country_name"}))
		Expect(analysis.Builtins).NotTo(BeEmpty())
	})
	It("does not assume an unmapped SQL Server session variable is stable", func() {
		analysis := scripts.Analyze(scripts.AnalysisOptions{SQL: "SELECT @@ROWCOUNT, @@SERVERNAME"})
		Expect(analysis.Blocked).To(BeEmpty())
		Expect(analysis.Unresolved).To(Equal([]string{"@@ROWCOUNT"}))
		Expect(analysis.Builtins).To(Equal([]sqlglot.BuiltinFunction{{Name: "@@SERVERNAME", Dependency: sqlglot.DependencyEnvironment}}))
	})
	DescribeTable("finds the largest call whose value depends only on the target",
		func(sql string, environment ...string) {
			analysis := scripts.Analyze(scripts.AnalysisOptions{SQL: sql})
			tree, err := sqlglot.ParseScript(sql, "tsql")
			Expect(err).NotTo(HaveOccurred())
			Expect(analysis.Blocked).To(BeEmpty())
			Expect(analysis.Environment).To(HaveExactElements(environment), string(tree.Tree.DumpJSON()))
		},
		Entry("the built-in itself", "SELECT DB_NAME() AS name", "DB_NAME()"),
		Entry("scalar selects around it", "DECLARE @c varchar(30); SET @c = (Select SUBSTRING((SELECT DB_NAME()),5,3))", "SUBSTRING(DB_NAME(), 5, 3)"),
		Entry("nested functions", "DECLARE @c varchar(10) = UPPER(SUBSTRING(DB_NAME(), 5, 3));", "UPPER(SUBSTRING(DB_NAME(), 5, 3))"),
		Entry("string functions", "SELECT LEFT(LTRIM(DB_NAME()), 3), RIGHT(REPLACE(DB_NAME(), N'_', ''), 2)", "LEFT(LTRIM(DB_NAME()), 3)", "RIGHT(REPLACE(DB_NAME(), N'_', ''), 2)"),
		Entry("a comparison stops it", "IF (SELECT SUBSTRING((select DB_NAME()), 5, 3)) != 'KEN' SELECT 1", "SUBSTRING(DB_NAME(), 5, 3)"),
		Entry("a variable, a column or a date stops it", "DECLARE @n int; SELECT SUBSTRING(DB_NAME(), @n, 3), REPLACE(DB_NAME(), name, ''), CONCAT(DB_NAME(), GETDATE()) FROM dbo.AsEnv", "DB_NAME()"),
		Entry("a select with a source stops it", "SELECT UPPER((SELECT DB_NAME() FROM dbo.AsEnv))", "DB_NAME()"),
		Entry("a constant argument", "SELECT DB_NAME(1)", "DB_NAME(1)"),
		Entry("a varying argument is not tracked", "DECLARE @id int; SELECT DB_NAME(@id)"),
		Entry("constant dynamic SQL", "EXEC(N'SELECT UPPER(DB_NAME())')", "UPPER(DB_NAME())"),
		Entry("no environment", "SELECT UPPER('x'), GETDATE()"),
	)
	It("frames scalar types, nulls and values without collisions", func() {
		values := []scripts.Scalar{
			{Type: "nvarchar", Length: 128, Null: true},
			{Type: "nvarchar", Length: 128},
			{Type: "nvarchar", Length: 128, Value: "ZW"},
			{Type: "nvarchar", Length: 128, Value: "ZW "},
			{Type: "varchar", Length: 128, Value: "ZW"},
			{Type: "nvarchar", Length: 128, Value: "ישראל"},
		}
		seen := map[string]bool{}
		for _, value := range values {
			hash := Must(value.Fingerprint("dbo.country"))
			Expect(seen[hash]).To(BeFalse())
			seen[hash] = true
			Expect(Must(value.Fingerprint("dbo.country"))).To(Equal(hash))
		}
		_, err := (scripts.Scalar{Type: "sql_variant"}).Fingerprint("dbo.country")
		Expect(err).To(HaveOccurred())
		AddReportEntry("scalar-encoding", map[string]int{"distinctValues": len(seen)})
	})
})
