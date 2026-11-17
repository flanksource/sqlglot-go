package sqlserver

import (
	catalog "github.com/flanksource/sqlglot-go/database/inspect/catalog/sqlserver"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Tracked function closure validation", func() {
	var modules map[string]moduleSnapshot
	var function Function
	BeforeEach(func() {
		function = Function{Name: "dbo.country", Type: "nvarchar"}
		modules = map[string]moduleSnapshot{
			"dbo.country": {Metadata: catalog.Module{Kind: "FN", ReturnType: "nvarchar"}, References: []string{"dbo.helper"}},
			"dbo.helper":  {Metadata: catalog.Module{Kind: "FN"}, Analysis: scripts.Analyze(scripts.AnalysisOptions{SQL: "CREATE FUNCTION dbo.helper() RETURNS nvarchar(10) AS BEGIN RETURN N'ZW' END", InvokedModule: true})},
		}
	})
	It("allows a bounded local string-valued function closure including recursion", func() {
		helper := modules["dbo.helper"]
		helper.References = []string{"dbo.country"}
		modules["dbo.helper"] = helper
		Expect(validateProbe(function, modules)).To(Succeed())
	})
	It("rejects time-dependent code hidden inside an unregistered helper", func() {
		modules["dbo.helper"] = moduleSnapshot{Metadata: catalog.Module{Kind: "FN"}, Analysis: scripts.Analyze(scripts.AnalysisOptions{SQL: "CREATE FUNCTION dbo.helper() RETURNS nvarchar(30) AS BEGIN RETURN CONVERT(nvarchar(30),GETDATE()) END", InvokedModule: true})}
		Expect(validateProbe(function, modules)).To(MatchError(ContainSubstring("GETDATE")))
	})
	It("rejects unsupported result types before executing the function", func() {
		function.Type = "sql_variant"
		Expect(validateProbe(function, modules)).To(MatchError(ContainSubstring("unsupported")))
	})
	It("rejects unresolved definition closure members", func() {
		delete(modules, "dbo.helper")
		Expect(validateProbe(function, modules)).To(MatchError(ContainSubstring("dbo.helper")))
	})
})
