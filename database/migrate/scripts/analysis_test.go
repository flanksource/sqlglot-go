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
	It("blocks unresolvable dynamic dispatch", func() {
		Expect(scripts.Analyze(scripts.AnalysisOptions{SQL: "EXEC(@sql)"}).Blocked).ToNot(BeEmpty())
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
	})
})
