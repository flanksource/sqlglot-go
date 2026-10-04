package sqlglot

import "testing"

// T-SQL statements a stored-procedure body or an application's inline SQL
// writes, beyond what the reference reads: EXEC arguments marked OUTPUT,
// statements ended by `;` or by the next statement, UPDATE … FROM with joins,
// and the ODBC `{fn …}` escape.

func TestTSQLBatchStatements(t *testing.T) {
	cases := []struct {
		sql     string
		classes []string
		written string
	}{
		{"EXEC dbo.p 1, @e = @e OUTPUT", []string{"Execute"}, "EXECUTE dbo.p 1, @e = @e OUTPUT"},
		{"EXEC p @e OUT;", []string{"Execute"}, "EXECUTE p @e OUTPUT"},
		{"EXEC p;", []string{"Execute"}, "EXECUTE p"},
		{"EXECUTE p 'a', @n = 2;", []string{"Execute"}, "EXECUTE p 'a', @n = 2"},
		{"DECLARE @e INT; EXEC p 1, @e = @e OUTPUT; SELECT @e;", []string{"Declare", "Execute", "Select"},
			"DECLARE @e INTEGER; EXECUTE p 1, @e = @e OUTPUT; SELECT @e"},
		{"DECLARE @n INT; SET @n = (SELECT COUNT(*) FROM t); SELECT @n", []string{"Declare", "Set", "Select"},
			"DECLARE @n INTEGER; SET @n = (SELECT COUNT(*) FROM t); SELECT @n"},
		{"MERGE INTO t AS t USING s AS s ON t.id = s.id WHEN MATCHED THEN UPDATE SET t.v = s.v; SELECT 1",
			[]string{"Merge", "Select"},
			"MERGE INTO t AS t USING s AS s ON t.id = s.id WHEN MATCHED THEN UPDATE SET t.v = s.v; SELECT 1"},
		{"SELECT a FROM t SELECT b FROM u", []string{"Select", "Select"}, "SELECT a FROM t; SELECT b FROM u"},
		{"DECLARE @x INT SET @x = 1 EXEC p @x", []string{"Declare", "Set", "Execute"},
			"DECLARE @x INTEGER; SET @x = 1; EXECUTE p @x"},
		{"UPDATE t SET a = 1 DELETE FROM u", []string{"Update", "Delete"}, "UPDATE t SET a = 1; DELETE FROM u"},
		// T-SQL's INTO is optional in MERGE, as the reference reads it.
		{"MERGE t USING s ON t.id = s.id WHEN MATCHED THEN DELETE", []string{"Merge"},
			"MERGE INTO t USING s ON t.id = s.id WHEN MATCHED THEN DELETE"},
		{"Merge @rep rpt USING (SELECT 1 AS a) s ON rpt.a = s.a WHEN MATCHED THEN UPDATE SET rpt.b = 1;", []string{"Merge"},
			"MERGE INTO @rep AS rpt USING (SELECT 1 AS a) AS s ON rpt.a = s.a WHEN MATCHED THEN UPDATE SET rpt.b = 1"},
		// So is INSERT's.
		{"Insert t(a, b) Values(@a, @b)", []string{"Insert"}, "INSERT INTO t (a, b) VALUES (@a, @b)"},
		{"INSERT t VALUES (1)", []string{"Insert"}, "INSERT INTO t VALUES (1)"},
		// A system function (@@ROWCOUNT) is a parameter of a parameter.
		{"SELECT @n = @@ROWCOUNT", []string{"Select"}, "SELECT @n = @@ROWCOUNT"},
		{"SELECT @n = @@ROWCOUNT, @e = @@ERROR", []string{"Select"}, "SELECT @n = @@ROWCOUNT, @e = @@ERROR"},
		{"EXEC @r = master.dbo.sp_OAMethod @o, 'x'", []string{"Execute"}, "EXECUTE @r = master.dbo.sp_OAMethod @o, 'x'"},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			tree, err := ParseOne(c.sql, "tsql")
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			statements := []*Expression{tree}
			if tree.Class == "Block" {
				statements, _ = tree.Args["expressions"].([]*Expression)
			}
			if len(statements) != len(c.classes) {
				t.Fatalf("read %d statements, want %v", len(statements), c.classes)
			}
			for i, statement := range statements {
				if statement.Class != c.classes[i] {
					t.Fatalf("statement %d is %s, want %s", i, statement.Class, c.classes[i])
				}
			}
			if written, err := Generate(tree, "tsql"); err != nil || written != c.written {
				t.Fatalf("wrote %q (%v), want %q", written, err, c.written)
			}
		})
	}
}

func TestTSQLExecuteOutputArgument(t *testing.T) {
	tree, err := ParseOne("EXEC p @a OUTPUT, @b = @c OUT", "tsql")
	if err != nil {
		t.Fatal(err)
	}
	args, _ := tree.Args["expressions"].([]*Expression)
	if len(args) != 2 {
		t.Fatalf("read %d arguments", len(args))
	}
	if args[0].Class != "OutputParameter" || args[0].This().Class != "Parameter" {
		t.Fatalf("positional OUTPUT argument read as %s", args[0].Class)
	}
	named, _ := args[1].Args["expression"].(*Expression)
	if args[1].Class != "EQ" || named == nil || named.Class != "OutputParameter" {
		t.Fatalf("named OUTPUT argument read as %s", args[1].Class)
	}
}

func TestTSQLExecuteRefusesTrailingTokens(t *testing.T) {
	for _, sql := range []string{"EXEC p 1 2", "EXEC p @a OUTPUT OUTPUT", "MERGE INTO t USING s ON t.id = s.id WHEN MATCHED THEN DELETE x"} {
		if tree, err := ParseOne(sql, "tsql"); err == nil {
			t.Errorf("%s: accepted as %s", sql, tree.Class)
		}
	}
}

func TestTSQLUpdateFromJoins(t *testing.T) {
	sql := "UPDATE a SET a.s = b.s FROM AsPolicy AS a JOIN AsClient AS b ON a.c = b.c WHERE a.p = 1"
	tree, err := ParseOne(sql, "tsql")
	if err != nil {
		t.Fatal(err)
	}
	from, _ := tree.Args["from_"].(*Expression)
	joins, _ := from.This().Args["joins"].([]*Expression)
	if len(joins) != 1 || joins[0].This().Name() != "AsClient" {
		t.Fatalf("FROM joins = %v", joins)
	}
	if written, err := Generate(tree, "tsql"); err != nil || written != sql {
		t.Fatalf("wrote %q (%v)", written, err)
	}
}

func TestTSQLODBCFunctionEscape(t *testing.T) {
	tree, err := ParseOne("SELECT SUBSTRING(CONVERT(VARCHAR, {fn curdate()}, 120), 1, 10)", "tsql")
	if err != nil {
		t.Fatal(err)
	}
	if calls := tree.FindAll("Anonymous"); len(calls) != 1 || calls[0].Name() != "curdate" {
		t.Fatalf("escaped call read as %v", calls)
	}
	for _, sql := range []string{"SELECT {fn curdate()", "SELECT {fn}"} {
		if _, err := ParseOne(sql, "tsql"); err == nil {
			t.Errorf("%s: accepted", sql)
		}
	}
}

// The T-SQL corpus never writes a bare FLOOR or a one-sided TRIM, so their
// spellings come from the probe's extra shapes rather than the corpus.
func TestTSQLWritesFloorAndOneSidedTrim(t *testing.T) {
	for _, c := range []struct{ sql, class, written string }{
		{"SELECT FLOOR(x)", "Floor", "SELECT FLOOR(x)"},
		{"SELECT FLOOR(RAND() * 10) + 1", "Floor", "SELECT FLOOR(RAND() * 10) + 1"},
		{"SELECT LTRIM(RTRIM(x))", "Trim", "SELECT LTRIM(RTRIM(x))"},
		{"SELECT RTRIM(x)", "Trim", "SELECT RTRIM(x)"},
	} {
		tree, err := ParseOne(c.sql, "tsql")
		if err != nil {
			t.Fatalf("%s: %v", c.sql, err)
		}
		if found := tree.FindAll(c.class); len(found) == 0 {
			t.Fatalf("%s: no %s node", c.sql, c.class)
		}
		if written, err := Generate(tree, "tsql"); err != nil || written != c.written {
			t.Errorf("%s wrote %q (%v), want %q", c.sql, written, err, c.written)
		}
	}
}

func TestTSQLDateDiffIntegerStartDate(t *testing.T) {
	// The reference reads an integer start date as that many days after
	// 1900-01-01, the T-SQL epoch, and goes on as it does for a string date.
	tree, err := ParseOne("SELECT DATEDIFF(yy, 0, GETDATE())", "tsql")
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT DATEDIFF(YEAR, CAST('1900-01-01' AS DATETIME2), CAST(GETDATE() AS DATETIME2))"
	if written, err := Generate(tree, "tsql"); err != nil || written != want {
		t.Fatalf("wrote %q (%v), want %q", written, err, want)
	}
}
