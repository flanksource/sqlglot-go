package sqlglot

import (
	"strings"
	"testing"
)

// Whole T-SQL routine definitions, as sys.sql_modules holds them: a CREATE
// [OR ALTER] PROCEDURE or FUNCTION whose body is a run of statements. Where
// the reference reads a form, the tree is the reference's; where it gives up
// or misreads one (a bodyless IF, RETURN, THROW, a multi-statement scalar
// function), the port reads a real node and writes it back as T-SQL.

func TestTSQLRoutineDefinitions(t *testing.T) {
	for _, c := range []struct{ name, sql, written string }{
		{"output, out and readonly parameters",
			"CREATE OR ALTER PROCEDURE acme.p @a INT = 5 OUTPUT, @b INT OUT, @t acme.ids READONLY AS SELECT 1",
			"CREATE OR ALTER PROCEDURE acme.p @a INTEGER = 5 OUTPUT, @b INTEGER OUT, @t acme.ids READONLY AS SELECT 1"},
		{"wrapped output parameter",
			"CREATE PROCEDURE acme.p (@a INT = 5 OUTPUT) AS SELECT 1",
			"CREATE PROCEDURE acme.p(@a INTEGER = 5 OUTPUT) AS SELECT 1"},
		{"PROC keeps its BEGIN",
			"CREATE PROC acme.p AS BEGIN SET NOCOUNT ON; SELECT 1; END",
			"CREATE PROC acme.p AS BEGIN SET NOCOUNT ON; SELECT 1; END"},
		{"statements without semicolons",
			"CREATE PROCEDURE acme.p AS BEGIN SET NOCOUNT ON SET XACT_ABORT ON SELECT 1 END",
			"CREATE PROCEDURE acme.p AS BEGIN SET NOCOUNT ON; SET XACT_ABORT ON; SELECT 1; END"},
		{"body to the end of the batch",
			"CREATE PROCEDURE acme.p AS SET NOCOUNT ON SELECT 1",
			"CREATE PROCEDURE acme.p AS SET NOCOUNT ON; SELECT 1"},
		{"IF NOT EXISTS RETURN",
			"CREATE PROCEDURE acme.p AS BEGIN IF NOT EXISTS (SELECT 1 FROM acme.orders) RETURN; SELECT 1; END",
			"CREATE PROCEDURE acme.p AS BEGIN IF NOT EXISTS(SELECT 1 FROM acme.orders) RETURN; SELECT 1; END"},
		{"IF BEGIN END ELSE BEGIN END",
			"CREATE PROCEDURE acme.p AS BEGIN IF @a > 1 BEGIN SELECT 1; END ELSE BEGIN SELECT 2; END END",
			"CREATE PROCEDURE acme.p AS BEGIN IF @a > 1 BEGIN SELECT 1; END ELSE BEGIN SELECT 2; END; END"},
		{"IF statement ELSE statement",
			"CREATE PROCEDURE acme.p AS BEGIN IF @a > 1 SELECT 1; ELSE SELECT 2 END",
			"CREATE PROCEDURE acme.p AS BEGIN IF @a > 1 SELECT 1 ELSE SELECT 2; END"},
		{"ELSE IF chain",
			"CREATE PROCEDURE acme.p AS BEGIN IF @a = 1 SELECT 1 ELSE IF @a = 2 SELECT 2 ELSE BEGIN SELECT 3 END END",
			"CREATE PROCEDURE acme.p AS BEGIN IF @a = 1 SELECT 1 ELSE IF @a = 2 SELECT 2 ELSE BEGIN SELECT 3; END; END"},
		{"RETURN with and without a value",
			"CREATE PROCEDURE acme.p AS BEGIN IF @a IS NULL RETURN 1 RETURN END",
			"CREATE PROCEDURE acme.p AS BEGIN IF @a IS NULL RETURN 1; RETURN; END"},
		{"THROW with arguments and bare",
			"CREATE PROCEDURE acme.p AS BEGIN THROW 50001, N'bad', 1; THROW; END",
			"CREATE PROCEDURE acme.p AS BEGIN THROW 50001, N'bad', 1; THROW; END"},
		{"PRINT with a structured expression",
			"CREATE PROCEDURE acme.p AS BEGIN PRINT N'hello' + @name; END",
			"CREATE PROCEDURE acme.p AS BEGIN PRINT N'hello' + @name; END"},
		{"nested BEGIN END and CASE END",
			"CREATE PROCEDURE acme.p AS BEGIN BEGIN SELECT CASE WHEN a = 1 THEN 1 END FROM t END END",
			"CREATE PROCEDURE acme.p AS BEGIN BEGIN SELECT CASE WHEN a = 1 THEN 1 END FROM t; END; END"},
		{"a national string is a string, not a word",
			"CREATE PROCEDURE acme.p AS BEGIN SET @a = N'on'; SET @b = 'x' RETURN END",
			"CREATE PROCEDURE acme.p AS BEGIN SET @a = N'on'; SET @b = 'x'; RETURN; END"},
		{"RETURN and IF are never an implicit alias",
			"CREATE PROCEDURE acme.p AS BEGIN SELECT a FROM t RETURN SELECT b FROM u IF @a = 1 RETURN END",
			"CREATE PROCEDURE acme.p AS BEGIN SELECT a FROM t; RETURN; SELECT b FROM u; IF @a = 1 RETURN; END"},
		{"temp tables",
			"CREATE PROCEDURE acme.p AS BEGIN CREATE TABLE #t (a INT) SELECT a INTO #u FROM #t DROP TABLE IF EXISTS #t DROP TABLE #u END",
			"CREATE PROCEDURE acme.p AS BEGIN CREATE TABLE #t (a INTEGER); SELECT a INTO #u FROM #t; DROP TABLE IF EXISTS #t; DROP TABLE #u; END"},
		{"BEGIN TRANSACTION is not a block",
			"CREATE PROCEDURE acme.p AS BEGIN BEGIN TRANSACTION; DELETE FROM t; COMMIT; END",
			"CREATE PROCEDURE acme.p AS BEGIN BEGIN TRANSACTION; DELETE FROM t; COMMIT TRANSACTION; END"},
		{"scalar function with a statement body",
			"CREATE FUNCTION acme.f(@a INT) RETURNS INT WITH SCHEMABINDING AS BEGIN DECLARE @b INT = 1; RETURN @a + @b; END",
			"CREATE FUNCTION acme.f(@a INTEGER) RETURNS INTEGER WITH SCHEMABINDING AS BEGIN DECLARE @b INTEGER = 1; RETURN @a + @b; END"},
		{"scalar function returning without a semicolon",
			"CREATE FUNCTION acme.f() RETURNS INT AS BEGIN RETURN 1 END",
			"CREATE FUNCTION acme.f() RETURNS INTEGER AS BEGIN RETURN 1; END"},
		{"inline table-valued function",
			"CREATE OR ALTER FUNCTION acme.f(@a INT) RETURNS TABLE AS RETURN (SELECT id FROM acme.orders WHERE id = @a)",
			"CREATE OR ALTER FUNCTION acme.f(@a INTEGER) RETURNS TABLE AS RETURN (SELECT id FROM acme.orders WHERE id = @a)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tree, err := ParseOne(c.sql, "tsql")
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if tree.Class != "Create" {
				t.Fatalf("read a %s, want a Create", tree.Class)
			}
			written, err := Generate(tree, "tsql")
			if err != nil || written != c.written {
				t.Fatalf("wrote %q (%v)\n                want %q", written, err, c.written)
			}
			again, err := ParseOne(written, "tsql")
			if err != nil {
				t.Fatalf("re-parse: %v", err)
			}
			if rewritten, err := Generate(again, "tsql"); err != nil || rewritten != written {
				t.Fatalf("not a fixed point: %q (%v)", rewritten, err)
			}
		})
	}
}

// The statement nodes a body is made of, which the reference has no reader
// for: a RETURN, a THROW, and an IF whose branch is a single statement.
func TestTSQLRoutineBodyNodes(t *testing.T) {
	tree, err := ParseOne("CREATE PROCEDURE acme.p AS BEGIN IF @a = 1 RETURN 2 ELSE THROW 50001, 'x', 1; RETURN END", "tsql")
	if err != nil {
		t.Fatal(err)
	}
	body := tree.Args["expression"].(*Expression).Args["expressions"].([]*Expression)
	var classes []string
	for _, statement := range body {
		classes = append(classes, statement.Class)
	}
	if got := strings.Join(classes, ","); got != "IfBlock,Return,EndStatement" {
		t.Fatalf("body is %s", got)
	}
	branch, _ := body[0].Args["true"].(*Expression)
	otherwise, _ := body[0].Args["false"].(*Expression)
	if branch == nil || branch.Class != "Return" || otherwise == nil || otherwise.Class != "Throw" {
		t.Fatalf("IF branches read as %v / %v", branch, otherwise)
	}
	if bare := body[1]; bare.Args["this"] != nil {
		t.Fatalf("a bare RETURN carries %v", bare.Args["this"])
	}
}

func TestTSQLRoutineRefusals(t *testing.T) {
	for _, c := range []struct{ name, sql string }{
		{"unterminated BEGIN", "CREATE PROCEDURE acme.p AS BEGIN SELECT 1"},
		{"END without BEGIN", "CREATE PROCEDURE acme.p AS SELECT 1 END"},
		{"two routines in one batch", "CREATE PROCEDURE acme.a AS SELECT 1 CREATE PROCEDURE acme.b AS SELECT 2"},
		{"two routines after END", "CREATE PROCEDURE acme.a AS BEGIN SELECT 1 END CREATE PROCEDURE acme.b AS SELECT 2"},
		{"unknown statement", "CREATE PROCEDURE acme.p AS BEGIN FROBNICATE @x; END"},
		{"bare word statement", "CREATE PROCEDURE acme.p AS BEGIN frobnicate; END"},
		{"PRINT without an expression", "CREATE PROCEDURE acme.p AS BEGIN PRINT; END"},
		{"IF without a statement", "CREATE PROCEDURE acme.p AS BEGIN IF @a = 1 END"},
		{"ELSE without IF", "CREATE PROCEDURE acme.p AS BEGIN ELSE SELECT 1 END"},
		{"THROW with two arguments", "CREATE PROCEDURE acme.p AS BEGIN THROW 50001, 'x'; END"},
		{"parameter mode twice", "CREATE PROCEDURE acme.p @a INT OUTPUT READONLY AS SELECT 1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if tree, err := ParseOne(c.sql, "tsql"); err == nil {
				written, _ := Generate(tree, "tsql")
				t.Fatalf("accepted as %s: %s", tree.Class, written)
			}
		})
	}
}
