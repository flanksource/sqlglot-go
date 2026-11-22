package scripts_test

import (
	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Script reads", func() {
	DescribeTable("are the queries answering what its statements read",
		func(sql string, queries ...string) {
			analysis := scripts.Analyze(scripts.AnalysisOptions{SQL: sql})
			Expect(analysis.UntrackedReads).To(BeEmpty())
			Expect(readQueries(analysis)).To(HaveExactElements(queries))
		},
		Entry("an IF is its condition's outcome", "IF NOT EXISTS (Select * from AsCode where CodeValue ='01')\nBEGIN INSERT INTO ASCODE VALUES ('01') END",
			"SELECT CASE WHEN NOT EXISTS (Select * from AsCode where CodeValue ='01') THEN 1 ELSE 0 END"),
		Entry("a variable assignment is the value, with its constants",
			"BEGIN\nDECLARE @n AS INT\nDECLARE @code AS VARCHAR(50) = 'AsCodeTitle'\nSELECT @n = COUNT(*) FROM AsCode WHERE CodeName = @code\nIF @n = 0\nINSERT INTO AsCode VALUES (@code)\nEND",
			"DECLARE @CODE VARCHAR(50) = 'AsCodeTitle';\nSELECT COUNT(*) FROM AsCode WHERE CodeName = @code"),
		Entry("a DELETE is the rows it matches", "DELETE FROM AsMapValue WHERE Guid IN (SELECT Guid FROM AsCode WHERE CodeName IN ('A'))",
			"SELECT * FROM AsMapValue WHERE Guid IN (SELECT Guid FROM AsCode WHERE CodeName IN ('A'))"),
		Entry("an UPDATE is the rows it matches", "UPDATE m SET Name='x' FROM AsMapValue m JOIN AsCode c ON c.Guid = m.Guid WHERE c.CodeName='A'",
			"SELECT * FROM AsMapValue AS m JOIN AsCode AS c ON c.Guid = m.Guid WHERE c.CodeName = 'A'"),
		Entry("a DELETE or UPDATE that reads only its target", "DELETE FROM AsCode WHERE CodeName = 'A'\nUPDATE AsCode SET CodeName = 'B' WHERE CodeName = 'C'"),
		Entry("an INSERT is its query", "INSERT INTO AsMapValue (Guid) SELECT CodeValue FROM AsCode WHERE CodeName='A'", "SELECT CodeValue FROM AsCode WHERE CodeName = 'A'"),
		Entry("a query with a generated column is the rows it matches", "INSERT INTO AsMapValue SELECT NEWID(), CodeValue FROM AsCode WHERE CodeName='A'", "SELECT * FROM AsCode WHERE CodeName = 'A'"),
		Entry("a SET is its subquery", "DECLARE @g varchar(40); SET @g = (SELECT TOP 1 CodeValue FROM AsCode WHERE CodeName='A')", "SELECT * FROM AsCode WHERE CodeName = 'A'"),
		Entry("each batch has its own constants", "DECLARE @c varchar(9) = 'A'\nSELECT 1 FROM AsCode WHERE CodeName = @c\nGO\nDECLARE @c varchar(9) = 'B'\nSELECT 1 FROM AsCode WHERE CodeName = @c",
			"DECLARE @C VARCHAR(9) = 'A';\nSELECT 1 FROM AsCode WHERE CodeName = @c", "DECLARE @C VARCHAR(9) = 'B';\nSELECT 1 FROM AsCode WHERE CodeName = @c"),
		Entry("nothing", "INSERT INTO AsCode VALUES ('A')"),
	)
	DescribeTable("are untracked when no query answers them on its own",
		func(sql string, queries ...string) {
			analysis := scripts.Analyze(scripts.AnalysisOptions{SQL: sql})
			Expect(analysis.UntrackedReads).To(Equal([]scripts.ObjectName{scripts.NewObjectName("dbo", "AsCode")}))
			Expect(readQueries(analysis)).To(HaveExactElements(queries))
		},
		Entry("a reassigned variable ", "DECLARE @c varchar(9) = 'A'\nSET @c = 'B'\nSELECT 1 FROM AsCode WHERE CodeName = @c"),
		Entry("a variable assigned by a query ", "DECLARE @c varchar(9) = 'A'\nSELECT @c = MAX(Name) FROM AsCode\nSELECT 1 FROM AsCode WHERE CodeName = @c", "SELECT MAX(Name) FROM AsCode"),
		Entry("a variable set through OUTPUT ", "DECLARE @c varchar(9) = 'A'\nEXEC dbo.load @c OUTPUT\nSELECT 1 FROM AsCode WHERE CodeName = @c"),
		Entry("a date ", "SELECT 1 FROM AsCode WHERE Created > GETDATE()"),
		Entry("a temporary table ", "SELECT 1 FROM AsCode WHERE CodeName IN (SELECT n FROM #names)"),
	)
	DescribeTable("are never a query with an effect of its own",
		func(sql string) {
			Expect(readQueries(scripts.Analyze(scripts.AnalysisOptions{SQL: sql}))).To(BeEmpty())
		},
		Entry("taking a sequence number", "INSERT INTO AsMapValue SELECT NEXT VALUE FOR dbo.AsSeq, CodeValue FROM AsCode WHERE CodeName='A'"),
		Entry("taking a sequence number in a condition", "IF EXISTS (SELECT 1 FROM AsCode) AND NEXT VALUE FOR dbo.AsSeq > 1 PRINT 'x'"),
		Entry("running SQL on a linked server", "INSERT INTO AsMapValue SELECT q.v FROM AsCode c JOIN OPENQUERY(other, 'EXEC dbo.load') q ON q.v = c.CodeValue"),
		Entry("running SQL on another source", "SELECT q.v FROM AsCode c CROSS JOIN OPENROWSET('MSOLEDBSQL', 'Server=other', 'EXEC dbo.load') q"),
		Entry("changing the rows it reads", "INSERT INTO AsMapValue SELECT d.Guid FROM (DELETE FROM AsCode OUTPUT deleted.Guid WHERE CodeName='A') AS d"),
		Entry("selecting into a table", "SELECT CodeValue INTO AsCodeCopy FROM AsCode"),
	)
})

func readQueries(analysis scripts.Analysis) []string {
	out := []string{}
	for _, read := range analysis.Reads {
		out = append(out, read.SQL)
	}
	return out
}
