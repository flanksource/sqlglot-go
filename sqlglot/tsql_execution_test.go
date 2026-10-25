package sqlglot

import (
	"reflect"
	"testing"
)

func TestTSQLExecutionRoutines(t *testing.T) {
	for _, c := range []struct {
		name, sql string
		classes   []string
	}{
		{"property", `CREATE PROCEDURE acme.get_by_property @property VARCHAR(64), @value VARCHAR(255) AS
BEGIN
  IF (@property IS NULL) OR (@property = '')
  BEGIN
    raiserror('Must specify a property name.',-1,-1)
    return (1)
  END
  SELECT @value
END`, []string{"IfBlock", "Select", "EndStatement"}},
		{"rename", `CREATE PROCEDURE acme.rename_item @id INT, @name SYSNAME AS
BEGIN
  DECLARE @current INT
  IF (@id IS NULL OR @name IS NULL)
  BEGIN
    RAISERROR (N'E_INVALIDARG', 16, 1);
    return -1
  END
  EXECUTE AS CALLER;
  SELECT @current = DATABASE_PRINCIPAL_ID();
  REVERT;
  IF (@current IS NULL)
  BEGIN
    RAISERROR ('Item does not exist or you do not have permission.', 16, 1)
    return -3
  END
  RAISERROR('Renamed %s', 10, 1, @name) WITH NOWAIT
END`, []string{"Declare", "IfBlock", "ExecuteAs", "Select", "Revert", "IfBlock", "Raiserror", "EndStatement"}},
		{"compile", `CREATE PROCEDURE acme.compile_all @flag INT AS
BEGIN
  DECLARE @sql1 NVARCHAR(4000), @sql2 NVARCHAR(4000), @lerror INT
  begin tran compile_all
  exec (@sql1)
  EXEC (@sql1 + @sql2)
  EXECUTE ('SELECT 1')
  IF @flag = 1 raiserror('', 16, -1) /* helper not loaded */
  select @lerror = @@error
  rollback tran compile_all
  RAISERROR (@lerror,16,-1)
  commit tran compile_all
END`, []string{"Declare", "Transaction", "Execute", "Execute", "Execute", "IfBlock", "Select", "Rollback", "Raiserror", "Commit", "EndStatement"}},
		{"options and variables", `CREATE PROC acme.p AS BEGIN
SELECT 1
rAiSeRrOr (@msg, @severity, @state, @arg, N'example') wItH log, nowait, seterror; -- example
revert
EXECUTE AS SELF
EXECUTE AS OWNER
execute as user = 'example'
execute as login = 'example'
RAISERROR ('example', +16, -1)
RETURN
END`, []string{"Select", "Raiserror", "Revert", "ExecuteAs", "ExecuteAs", "ExecuteAs", "ExecuteAs", "Raiserror", "Return", "EndStatement"}},
		{"transaction boundaries", `CREATE PROC acme.p AS BEGIN
BEGIN TRAN example
SELECT 1
COMMIT TRAN example
SELECT 2
ROLLBACK TRAN example
RAISERROR ('example', 16, 1)
END`, []string{"Transaction", "Select", "Commit", "Select", "Rollback", "Raiserror", "EndStatement"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			tree, err := ParseOne(c.sql, "tsql")
			if err != nil {
				t.Fatal(err)
			}
			result, err := ParseScript(c.sql, "tsql")
			if err != nil || !result.Complete || len(result.Diagnostics) != 0 || result.Tree == nil || result.Tree.Class != "Create" {
				t.Fatalf("script: %+v (%v)", result, err)
			}
			if tree.Class != "Create" || !tree.Equal(result.Tree) {
				t.Fatal("ParseOne and ParseScript must return the same Create")
			}
			body := tree.Args["expression"].(*Expression).Args["expressions"].([]*Expression)
			var classes []string
			for _, statement := range body {
				classes = append(classes, statement.Class)
			}
			if !reflect.DeepEqual(classes, c.classes) {
				t.Fatalf("body: %v, want %v", classes, c.classes)
			}
			for _, branch := range tree.FindAll("IfBlock") {
				if len(branch.FindAll("Raiserror")) != 1 {
					t.Fatal("IF branch must contain a typed Raiserror")
				}
			}
			written, err := Generate(tree, "tsql")
			if err != nil {
				t.Fatal(err)
			}
			again, err := ParseOne(written, "tsql")
			if err != nil {
				t.Fatalf("reparse %s: %v", written, err)
			}
			if rewritten, err := Generate(again, "tsql"); err != nil || rewritten != written {
				t.Fatalf("not stable: %s (%v), want %s", rewritten, err, written)
			}
		})
	}
}

func TestTSQLExecutionRefusals(t *testing.T) {
	for _, statement := range []string{
		"RAISERROR ('x')", "RAISERROR ('x', 16)", "RAISERROR ()",
		"RAISERROR ('x', 16, 1) WITH FROBNICATE", "RAISERROR ('x', 16, 1) WITH",
		"RAISERROR ('x', 16, 1) WITH LOG,", "RAISERROR ('x', 16, 1,)",
		"EXECUTE AS", "EXECUTE AS FROBNICATE", "EXECUTE AS USER", "EXECUTE AS LOGIN =",
		"EXEC ()", "EXEC (@sql", "REVERT FROBNICATE",
	} {
		t.Run(statement, func(t *testing.T) {
			sql := "CREATE PROC acme.p AS BEGIN " + statement + "; END"
			if tree, err := ParseOne(sql, "tsql"); err == nil {
				t.Fatalf("accepted as %s", tree.Class)
			}
			if result, err := ParseScript(sql, "tsql"); err == nil && result.Complete && len(result.Diagnostics) == 0 {
				t.Fatal("script accepted malformed statement")
			}
		})
	}
}

func TestTSQLExecutionStatementValues(t *testing.T) {
	for _, c := range []struct{ sql, class, written string }{
		{"raiserror(N'example %s', -1, @state, @arg) with log, nowait, seterror", "Raiserror", "RAISERROR (N'example %s', -1, @state, @arg) WITH LOG, NOWAIT, SETERROR"},
		{"exec (@a + @b)", "Execute", "EXECUTE (@a + @b)"},
		{"execute ('SELECT 1')", "Execute", "EXECUTE ('SELECT 1')"},
		{"execute as caller", "ExecuteAs", "EXECUTE AS CALLER"},
		{"execute as self", "ExecuteAs", "EXECUTE AS SELF"},
		{"execute as owner", "ExecuteAs", "EXECUTE AS OWNER"},
		{"execute as user = 'example'", "ExecuteAs", "EXECUTE AS USER = 'example'"},
		{"execute as login = 'example'", "ExecuteAs", "EXECUTE AS LOGIN = 'example'"},
		{"revert", "Revert", "REVERT"},
		{"begin tran example", "Transaction", "BEGIN TRANSACTION example"},
		{"commit tran example", "Commit", "COMMIT TRANSACTION example"},
		{"rollback tran example", "Rollback", "ROLLBACK TRANSACTION example"},
	} {
		t.Run(c.sql, func(t *testing.T) {
			tree, err := ParseOne("CREATE PROC acme.p AS BEGIN "+c.sql+"; END", "tsql")
			if err != nil {
				t.Fatal(err)
			}
			statement := tree.Args["expression"].(*Expression).Args["expressions"].([]*Expression)[0]
			if statement.Class != c.class {
				t.Fatalf("class %s, want %s", statement.Class, c.class)
			}
			if written, err := Generate(statement, "tsql"); err != nil || written != c.written {
				t.Fatalf("wrote %q (%v), want %q", written, err, c.written)
			}
			if c.class == "Execute" && statement.This().Class != "Paren" {
				t.Fatal("dynamic SQL must be a structured parenthesized expression")
			}
		})
	}
}
