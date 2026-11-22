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

var _ = Describe("Catalog routine effects", func() {
	const (
		procedure = "dbo.copy_codes"
		copyCodes = "CREATE PROCEDURE " + procedure + " AS\nINSERT dbo.CodeCopy SELECT Code FROM dbo.Codes"
	)
	codes := catalog.ModuleReference{Schema: "dbo", Name: "Codes", Kind: "U", Bound: true}
	codeCopy := catalog.ModuleReference{Schema: "dbo", Name: "CodeCopy", Kind: "U", Bound: true}

	DescribeTable("classifies references using the procedure body when its effects are known",
		func(definition string, refs []catalog.ModuleReference, want scripts.Effects) {
			epoch := newObservationEpoch(nil)
			epoch.names[procedure] = catalog.Module{Kind: "P"}
			epoch.loaded[procedure] = loadedModule{refs: refs, analysis: scripts.CachedAnalysis{
				Analysis: scripts.Analyze(scripts.AnalysisOptions{SQL: definition, InvokedModule: true}),
			}}
			Expect(epoch.moduleEffects()).To(Equal(map[scripts.ObjectName]scripts.Effects{procedure: want}))
		},
		Entry("separate read and write targets", copyCodes, []catalog.ModuleReference{codes, codeCopy},
			scripts.Effects{Reads: []scripts.ObjectName{"dbo.codes"}, Writes: []scripts.ObjectName{"dbo.codecopy"}}),
		Entry("an object used in both roles", "CREATE PROCEDURE "+procedure+" AS INSERT dbo.Codes SELECT Code FROM dbo.Codes",
			[]catalog.ModuleReference{codes}, scripts.Effects{Reads: []scripts.ObjectName{"dbo.codes"}, Writes: []scripts.ObjectName{"dbo.codes"}}),
		Entry("a reference found only in the catalog", copyCodes,
			[]catalog.ModuleReference{codes, codeCopy, {Schema: "legacy", Name: "Codes", Kind: "V", Bound: true}},
			scripts.Effects{Reads: []scripts.ObjectName{"dbo.codes", "legacy.codes"}, Writes: []scripts.ObjectName{"dbo.codecopy", "legacy.codes"}}),
		Entry("a body with unknown effects", "CREATE PROCEDURE "+procedure+" @sql nvarchar(max) AS\nINSERT dbo.CodeCopy SELECT Code FROM dbo.Codes\nEXEC (@sql)",
			[]catalog.ModuleReference{codes, codeCopy}, scripts.Effects{
				Reads: []scripts.ObjectName{"dbo.codecopy", "dbo.codes"}, Writes: []scripts.ObjectName{"dbo.codecopy", "dbo.codes"},
				Unknown: []string{"dbo.copy_codes: line 3: unsupported SQL statement: dynamic SQL"},
			}),
	)
})
