package sqlglot

import (
	"errors"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestParseScript(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Script parsing")
}

var _ = Describe("ParseScript", func() {
	It("parses a sequence followed by SELECT and DELETE batches", func() {
		sql := "CREATE SEQUENCE seq AS BIGINT\r\nGO\r\nSELECT 1;\r\nDELETE t WHERE x = 1\r\nGO"
		result, err := ParseScript(sql, "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Complete).To(BeTrue())
		Expect(result.Diagnostics).To(BeEmpty())
		Expect(result.Tree.Class).To(Equal("Block"))
		statements := result.Tree.Args["expressions"].([]*Expression)
		Expect([]string{statements[0].Class, statements[1].Class, statements[2].Class}).To(Equal([]string{"Create", "Select", "Delete"}))
	})

	It("returns a complete tree for supported statements", func() {
		result, err := ParseScript("SELECT 1; SELECT 2", "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Complete).To(BeTrue())
		Expect(result.Diagnostics).To(BeEmpty())
		Expect(result.Tree).NotTo(BeNil())
		Expect(result.Tree.Class).To(Equal("Block"))
	})

	It("parses GO-delimited INSERT batches without treating GO as SQL", func() {
		result, err := ParseScript("INSERT INTO t (a) VALUES (1)\nGO\nINSERT INTO t (a) VALUES (2)\nGO", "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Complete).To(BeTrue())
		Expect(result.Diagnostics).To(BeEmpty())
		Expect(result.Tree.Class).To(Equal("Block"))
	})

	It("parses a top-level procedure without classifying it as a nested routine", func() {
		result, err := ParseScript("CREATE PROCEDURE p AS BEGIN SELECT 1 END\nGO", "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Complete).To(BeTrue())
		Expect(result.Diagnostics).To(BeEmpty())
		Expect(result.Tree.Class).To(Equal("Create"))
	})

	It("keeps statements after a procedure's END inside its GO batch", func() {
		sql := "CREATE PROCEDURE p AS BEGIN SELECT 1 END; COMMIT\nGO\nSELECT 2"
		result, err := ParseScript(sql, "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Diagnostics).To(BeEmpty())
		statements := result.Tree.Args["expressions"].([]*Expression)
		Expect(statements).To(HaveLen(2))
		body := statements[0].Args["expression"].(*Expression)
		bodyStatements := body.Args["expressions"].([]*Expression)
		Expect(bodyStatements[len(bodyStatements)-1].Class).To(Equal("Commit"))
	})

	It("parses PRINT expressions in scripts and routine bodies", func() {
		for _, sql := range []string{
			"PRINT N'hello' + @name; SELECT 1",
			"PRINT @name PRINT N'done'",
			"CREATE PROCEDURE p AS BEGIN PRINT CONCAT(N'hello', @name); SELECT 1; END",
		} {
			result, err := ParseScript(sql, "tsql")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Diagnostics).To(BeEmpty(), sql)
			Expect(result.Complete).To(BeTrue())
			Expect(result.Tree).NotTo(BeNil())
		}
		tree, err := ParseOne("PRINT N'hello' + @name", "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(tree.Class).To(Equal("Print"))
		Expect(tree.Args["this"]).To(BeAssignableToTypeOf(&Expression{}))
		Expect(IsWrite(tree)).To(BeTrue())
		written, err := Generate(tree, "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(written).To(Equal("PRINT N'hello' + @name"))
	})

	It("separates transaction commands and consecutive DML without semicolons", func() {
		sql := "BEGIN TRANSACTION\nUPDATE t SET x = 1 WHERE id = 2;\nINSERT INTO t (x) VALUES (3)\nINSERT INTO t (x) VALUES (4)\nCOMMIT"
		result, err := ParseScript(sql, "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Diagnostics).To(BeEmpty())
		Expect(result.Complete).To(BeTrue())
		Expect(result.Tree.Class).To(Equal("Block"))
		statements := result.Tree.Args["expressions"].([]*Expression)
		classes := make([]string, len(statements))
		for i, statement := range statements {
			classes[i] = statement.Class
		}
		Expect(classes).To(Equal([]string{"Transaction", "Update", "Insert", "Insert", "Commit"}))
	})

	It("parses T-SQL DELETE without the optional FROM keyword", func() {
		result, err := ParseScript("DELETE AsCode WHERE CodeName = 'example'; SELECT 1", "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Diagnostics).To(BeEmpty())
		Expect(result.Tree.Class).To(Equal("Block"))
		deleted := result.Tree.Args["expressions"].([]*Expression)[0]
		Expect(deleted.Class).To(Equal("Delete"))
		Expect(deleted.Args["this"]).To(BeAssignableToTypeOf(&Expression{}))
		written, err := Generate(deleted, "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(written).To(Equal("DELETE FROM AsCode WHERE CodeName = 'example'"))
		_, err = ParseOne("DELETE AsCode WHERE CodeName = 'example'", "postgres")
		Expect(err).To(HaveOccurred())
	})

	It("parses WHILE with a block or a single statement", func() {
		for _, sql := range []string{
			"WHILE @i < 3 PRINT @i",
			"CREATE PROCEDURE p AS BEGIN WHILE @i < 3 BEGIN SET @i = @i + 1; END; SELECT @i; END",
		} {
			result, err := ParseScript(sql, "tsql")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Diagnostics).To(BeEmpty(), sql)
			Expect(result.Tree).NotTo(BeNil())
		}
		tree, err := ParseOne("WHILE @i < 3 PRINT @i", "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(tree.Class).To(Equal("While"))
		written, err := Generate(tree, "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(written).To(Equal("WHILE @i < 3 PRINT @i"))
	})

	It("parses TRY CATCH with typed statements and round trips its clauses", func() {
		sql := "BEGIN TRY UPDATE t SET x = 1; SELECT 1 END TRY BEGIN CATCH SELECT ERROR_NUMBER(), ERROR_MESSAGE(); END CATCH"
		result, err := ParseScript(sql, "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Diagnostics).To(BeEmpty())
		Expect(result.Tree.Class).To(Equal("TryCatch"))
		tryBlock := result.Tree.Args["try"].(*Expression)
		catchBlock := result.Tree.Args["catch"].(*Expression)
		Expect(tryBlock.Args["expressions"].([]*Expression)).To(HaveLen(2))
		Expect(catchBlock.Args["expressions"].([]*Expression)).To(HaveLen(1))
		written, err := Generate(result.Tree, "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(written).To(ContainSubstring("END TRY BEGIN CATCH"))
		reparsed, err := ParseScript(written, "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(reparsed.Diagnostics).To(BeEmpty())
		Expect(reparsed.Tree.Class).To(Equal("TryCatch"))
	})

	It("rejects a TRY clause without its CATCH clause", func() {
		result, err := ParseScript("BEGIN TRY SELECT 1 END TRY SELECT 2", "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Diagnostics).NotTo(BeEmpty())
	})

	It("parses the cursor lifecycle and its FETCH target variables", func() {
		sql := "DECLARE db_cursor CURSOR READ_ONLY FOR SELECT x FROM t\nOPEN db_cursor\nFETCH NEXT FROM db_cursor INTO @x\nCLOSE db_cursor\nDEALLOCATE db_cursor"
		result, err := ParseScript(sql, "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Diagnostics).To(BeEmpty())
		Expect(result.Tree.Class).To(Equal("Block"))
		statements := result.Tree.Args["expressions"].([]*Expression)
		classes := make([]string, len(statements))
		for i, statement := range statements {
			classes[i] = statement.Class
		}
		Expect(classes).To(Equal([]string{"DeclareCursor", "OpenCursor", "FetchCursor", "CloseCursor", "DeallocateCursor"}))
		written, err := Generate(result.Tree, "tsql")
		Expect(err).NotTo(HaveOccurred())
		reparsed, err := ParseScript(written, "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(reparsed.Diagnostics).To(BeEmpty())
	})

	It("accepts adjacent BEGIN blocks as separate T-SQL statements", func() {
		result, err := ParseScript("BEGIN SELECT 1 END\nBEGIN SELECT 2 END", "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Diagnostics).To(BeEmpty())
		Expect(result.Tree.Args["expressions"].([]*Expression)).To(HaveLen(2))
	})

	It("accepts a semicolon after a T-SQL routine body", func() {
		for _, sql := range []string{
			"CREATE FUNCTION f(@x INT) RETURNS INT AS BEGIN RETURN @x; END;",
			"CREATE PROCEDURE p AS BEGIN SELECT 1 END;",
		} {
			result, err := ParseScript(sql, "tsql")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Diagnostics).To(BeEmpty(), sql)
			Expect(result.Tree.Class).To(Equal("Create"))
		}
	})

	It("accepts a semicolon after ALTER COLUMN", func() {
		result, err := ParseScript("ALTER TABLE t ALTER COLUMN c [varchar](100) NULL;", "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Diagnostics).To(BeEmpty())
		Expect(result.Tree.Class).To(Equal("Alter"))
	})

	It("parses ALTER FUNCTION and preserves the ALTER verb when writing SQL", func() {
		result, err := ParseScript("ALTER FUNCTION dbo.f(@x INT) RETURNS INT AS BEGIN RETURN @x END", "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Diagnostics).To(BeEmpty())
		Expect(result.Tree.Class).To(Equal("Create"))
		written, err := Generate(result.Tree, "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(written).To(HavePrefix("ALTER FUNCTION "))
	})

	It("keeps a function BEGIN body attached when AS is omitted", func() {
		for _, sql := range []string{
			"CREATE FUNCTION dbo.f(@x INT) RETURNS INT BEGIN RETURN @x END",
			"CREATE FUNCTION dbo.g(@x INT) RETURNS @result TABLE (value INT) BEGIN INSERT INTO @result (value) VALUES (@x); RETURN END",
		} {
			result, err := ParseScript(sql, "tsql")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Diagnostics).To(BeEmpty(), sql)
			Expect(result.Tree.Class).To(Equal("Create"), sql)
			Expect(result.Tree.Args["expression"].(*Expression).Class).To(Equal("Block"), sql)
		}
	})

	It("treats FETCH NEXT FROM after SELECT as a cursor statement", func() {
		result, err := ParseScript("BEGIN SELECT 1 FETCH NEXT FROM c INTO @x END", "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Diagnostics).To(BeEmpty())
		statements := result.Tree.Args["expressions"].([]*Expression)
		Expect(statements[1].Class).To(Equal("FetchCursor"))
	})

	It("accepts DATE as an implicit T-SQL projection alias", func() {
		result, err := ParseScript("SELECT 1 Date, 2 Time", "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Diagnostics).To(BeEmpty())
		projections := result.Tree.Args["expressions"].([]*Expression)
		Expect(projections[0].Class).To(Equal("Alias"))
		Expect(projections[1].Class).To(Equal("Alias"))
	})

	It("parses a T-SQL sequence integer type and numeric options", func() {
		result, err := ParseScript("CREATE SEQUENCE dbo.s AS BIGINT START WITH 1 INCREMENT BY 1 CACHE", "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Diagnostics).To(BeEmpty())
		written, err := Generate(result.Tree, "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(written).To(ContainSubstring("AS BIGINT START WITH 1"))
	})

	It("accepts COMMENT as an implicit T-SQL projection alias", func() {
		result, err := ParseScript("SELECT ' ' Comment, 1 AS other_value", "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Diagnostics).To(BeEmpty())
		Expect(result.Tree.Args["expressions"].([]*Expression)[0].Class).To(Equal("Alias"))
	})

	It("parses an OUTPUT target column list", func() {
		result, err := ParseScript("UPDATE t SET x = 1 OUTPUT 1 INTO @updated (Result) WHERE id = 2", "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Diagnostics).To(BeEmpty())
		written, err := Generate(result.Tree, "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(written).To(ContainSubstring("INTO @updated (Result)"))
	})

	It("parses the TEXTIMAGE_ON filegroup of a T-SQL table", func() {
		result, err := ParseScript("CREATE TABLE t (body NTEXT) ON [PRIMARY] TEXTIMAGE_ON [PRIMARY]", "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Diagnostics).To(BeEmpty())
		written, err := Generate(result.Tree, "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(written).To(ContainSubstring("TEXTIMAGE_ON [PRIMARY]"))
	})

	It("ends a signless SET value before a CREATE statement", func() {
		result, err := ParseScript("SET QUOTED_IDENTIFIER ON\nCREATE TABLE t (id INT)", "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Diagnostics).To(BeEmpty())
		Expect(result.Tree.Args["expressions"].([]*Expression)).To(HaveLen(2))
	})

	It("rejects PRINT without exactly one expression", func() {
		for _, sql := range []string{"PRINT", "PRINT; SELECT 1", "PRINT 1, 2"} {
			result, err := ParseScript(sql, "tsql")
			Expect(err).NotTo(HaveOccurred(), sql)
			Expect(result.Diagnostics).NotTo(BeEmpty(), sql)
		}
	})

	It("rejects a script containing only separators", func() {
		_, err := ParseScript("GO\n;", "tsql")
		Expect(err).To(MatchError("empty SQL input"))
	})

	It("parses a sequence and DELETE inside a block", func() {
		sql := "BEGIN\n  DECLARE @x INT\n  CREATE SEQUENCE seq AS BIGINT\n  DELETE t WHERE x = 1\nEND\nGO\nSELECT 1"
		result, err := ParseScript(sql, "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Complete).To(BeTrue())
		Expect(result.Diagnostics).To(BeEmpty())
		Expect(result.Tree.Class).To(Equal("Block"))
		Expect(result.Tree.Args["expressions"].([]*Expression)).To(HaveLen(2))
	})

	It("reports two routine definitions without a GO separator", func() {
		result, err := ParseScript("CREATE PROCEDURE acme.a AS BEGIN SELECT 1 END CREATE PROCEDURE acme.b AS SELECT 2", "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Diagnostics).NotTo(BeEmpty())
	})

	It("attributes an invalid PRINT in an ELSE branch to PRINT rather than its enclosing END", func() {
		sql := "BEGIN\nIF 1 = 0\nBEGIN\nSELECT 1\nEND\nELSE\nBEGIN\nPRINT\nEND\nEND"
		result, err := ParseScript(sql, "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Diagnostics).To(ContainElement(And(
			HaveField("StatementKind", "PRINT"),
			HaveField("Label", ContainSubstring("PRINT without an expression")),
			HaveField("StatementStart", BeNumerically(">", 40)),
		)))
	})

	It("attributes an unknown routine statement to its own first word", func() {
		result, err := ParseScript("BEGIN\nSET NOCOUNT ON;\nFROBNICATE orders;\nEND", "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Diagnostics).To(HaveLen(1))
		Expect(result.Diagnostics[0].StatementKind).To(Equal("FROBNICATE"))
		Expect(result.Diagnostics[0].StatementStart).To(Equal(result.Diagnostics[0].Start))
	})

	It("preserves the first offending token position in parser errors", func() {
		_, err := ParseOne("BEGIN DECLARE @x INT END", "tsql")
		var unsupported *UnsupportedError
		Expect(err).To(HaveOccurred())
		Expect(errors.As(err, &unsupported)).To(BeTrue())
		Expect(unsupported.HasRange).To(BeTrue())
		Expect(unsupported.Start).To(Equal(0))
		Expect(unsupported.End).To(Equal(4))
	})
})
