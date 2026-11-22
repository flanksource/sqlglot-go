package scripts_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
)

var _ = Describe("Analyze effects", func() {
	const getCountry = `CREATE OR ALTER FUNCTION dbo.get_country()
RETURNS varchar(2)
AS
BEGIN
    RETURN (SELECT TOP 1 Country FROM dbo.AsEnv WHERE Environment = 'current')
END`

	analyze := func(sql string) scripts.Analysis {
		return scripts.Analyze(scripts.AnalysisOptions{SQL: sql})
	}

	DescribeTable("reads, writes and calls of a deployment script, as lower-cased schema-qualified names",
		func(sql string, expected scripts.Effects) {
			Expect(analyze(sql).Effects).To(Equal(expected))
		},
		Entry("a CTE is no object, the table it reads is",
			"WITH c AS (SELECT * FROM dbo.AsCode) INSERT INTO dbo.AsCodeCopy SELECT * FROM c",
			scripts.Effects{Reads: names("dbo.ascode"), Writes: names("dbo.ascodecopy")}),
		Entry("UPDATE by alias writes the FROM table it names",
			"UPDATE a SET Country = 'ZW' FROM dbo.AsEnv a WHERE a.Environment = 'current'",
			scripts.Effects{Writes: names("dbo.asenv")}),
		Entry("MERGE writes its target and reads its source",
			"MERGE INTO AsEnv AS t USING staging.AsEnv AS s ON t.Id = s.Id WHEN MATCHED THEN UPDATE SET t.Country = s.Country;",
			scripts.Effects{Reads: names("staging.asenv"), Writes: names("dbo.asenv")}),
		Entry("EXEC is a call", "EXEC dbo.mc_Refresh @Force = 1", scripts.Effects{Calls: names("dbo.mc_refresh")}),
		Entry("a name in another database keeps it as a prefix",
			"INSERT INTO dbo.AsCode SELECT * FROM Archive.dbo.AsCode", scripts.Effects{Reads: names("archive.dbo.ascode"), Writes: names("dbo.ascode")}),
		Entry("temp tables and table variables are no objects",
			"DECLARE @t TABLE (id int)\nSELECT id INTO #ids FROM dbo.AsCode\nINSERT INTO @t SELECT id FROM #ids",
			scripts.Effects{Reads: names("dbo.ascode")}),
		Entry("DDL writes the table, an index its table, a foreign key reads its table",
			"IF OBJECT_ID('dbo.AsEnv', 'U') IS NULL\nCREATE TABLE dbo.AsEnv (Id int REFERENCES dbo.AsCountry (Id))\nCREATE INDEX IX_AsEnv ON dbo.AsEnv (Id)",
			scripts.Effects{Reads: names("dbo.ascountry"), Writes: names("dbo.asenv")}),
		Entry("a table type is written as the type it creates",
			"CREATE TYPE dbo.FieldList AS TABLE (Name int NOT NULL PRIMARY KEY)", scripts.Effects{Writes: names("dbo.fieldlist")}),
		Entry("a CREATE FUNCTION writes the function, not what its body reads",
			getCountry, scripts.Effects{Writes: names("dbo.get_country")}),
		Entry("a script that touches no data has no effects", "SET NOCOUNT ON\nPRINT 'done'", scripts.Effects{}),
		Entry("a comment-only script has no effects", "-- nothing to deploy\n/* yet */", scripts.Effects{}),
	)

	DescribeTable("records a statement whose effects are unknown with its line",
		func(sql string, unknown string) {
			effects := analyze(sql).Effects
			Expect(effects.Unknown).To(Equal([]string{unknown}))
			Expect(effects.Reads).To(Equal(names("dbo.ascode")))
		},
		Entry("dynamic SQL in a variable",
			"DECLARE @sql nvarchar(max) = N'DELETE FROM dbo.X'\nEXEC (@sql)\nSELECT * FROM dbo.AsCode", "line 2: unsupported SQL statement: dynamic SQL"),
		Entry("dynamic SQL in an sp_executesql variable",
			"SELECT * FROM dbo.AsCode\nEXEC sp_executesql @sql", "line 2: unsupported SQL statement: dynamic SQL"),
	)

	It("follows dynamic SQL in a string literal, a routine it defines being a write", func() {
		analysis := analyze("EXEC('UPDATE dbo.AsCode SET Value = ''x''')\nEXEC('" + strings.ReplaceAll(getCountry, "'", "''") + "')")

		Expect(analysis.Effects).To(Equal(scripts.Effects{Writes: names("dbo.ascode", "dbo.get_country")}))
		Expect(analysis.Defines).To(Equal(map[scripts.ObjectName]scripts.Effects{"dbo.get_country": {Reads: names("dbo.asenv")}}))
	})

	It("keeps a script's own effects apart from the body effects of each routine it creates", func() {
		analysis := analyze(getCountry + "\nGO\nCREATE PROCEDURE app.Sync AS EXEC sp_executesql @sql\nGO\nUPDATE dbo.AsEnv SET Country = dbo.get_country()")

		Expect(analysis.Effects).To(Equal(scripts.Effects{
			Calls:   names("dbo.get_country"),
			Writes:  names("app.sync", "dbo.asenv", "dbo.get_country"),
			Unknown: []string{"line 8: unsupported SQL statement: dynamic SQL"},
		}))
		Expect(analysis.Defines).To(Equal(map[scripts.ObjectName]scripts.Effects{
			"dbo.get_country": {Reads: names("dbo.asenv")},
			"app.sync":        {Unknown: []string{"line 8: unsupported SQL statement: dynamic SQL"}},
		}))
	})

	It("records a routine defined twice as unknown", func() {
		analysis := analyze(getCountry + "\nGO\n" + getCountry)

		Expect(analysis.Effects.Unknown).To(Equal([]string{"line 8: routine dbo.get_country is defined more than once"}))
	})

	It("gives an invoked module definition its body effects", func() {
		analysis := scripts.Analyze(scripts.AnalysisOptions{SQL: getCountry, InvokedModule: true})

		Expect(analysis.Effects).To(Equal(scripts.Effects{Reads: names("dbo.asenv")}))
		Expect(analysis.Defines).To(BeNil())
	})

	It("records a script that does not split into batches as unknown", func() {
		Expect(analyze("SELECT * FROM dbo.AsCode\nGO 2").Effects.Unknown).To(ConsistOf(ContainSubstring("GO must stand alone")))
	})
})

func names(objects ...string) []scripts.ObjectName {
	out := make([]scripts.ObjectName, 0, len(objects))
	for _, object := range objects {
		out = append(out, scripts.ObjectName(object))
	}
	return out
}
