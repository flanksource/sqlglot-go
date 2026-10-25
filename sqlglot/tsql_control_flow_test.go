package sqlglot

import (
	"strings"
	"testing"
)

func TestTSQLControlFlow(t *testing.T) {
	for _, c := range []struct{ name, sql, classes string }{
		{"count up", `CREATE PROCEDURE acme.count_up AS
BEGIN
  DECLARE @i INT
  SET @i = 0
  WHILE 1 = 1
  BEGIN
    SET @i = @i + 1
    IF @i > 10 BREAK
    IF @i = 5 CONTINUE
    IF (@i = 9)
      goto TooMany
  END
Leave:
  return
TooMany:
  set @i = 0
  goto Leave
END`, "Declare,Set,While,Label,Return,Label,Set,Goto,EndStatement"},
		{"sync", `CREATE PROCEDURE acme.sync @flag INT AS
  DECLARE @rc INT
  EXEC @rc = acme.step_one @flag
  IF @rc <> 0 GOTO E_Fail
  IF @flag = 1
  BEGIN
    EXEC @rc = acme.step_two @flag, 'x'
    IF @rc <> 0 GOTO E_Fail
  END
  ELSE
  BEGIN
    SELECT 1
  END
CleanUp:
  return

E_Fail:
  exec acme.report @flag, @rc
  GOTO CleanUp`, "Declare,Execute,IfBlock,IfBlock,Label,Return,Label,Execute,Goto"},
		{"split ids", `CREATE FUNCTION acme.split_ids (@s NVARCHAR(400))
RETURNS @ids TABLE (id UNIQUEIDENTIFIER)
AS
BEGIN
  WHILE LEN(@s) > 0
  BEGIN
    IF CHARINDEX(',', @s) = 0
    BEGIN
      INSERT INTO @ids VALUES (CONVERT(UNIQUEIDENTIFIER, @s))
      BREAK
    END
    SET @s = SUBSTRING(@s, CHARINDEX(',', @s) + 1, 400)
  END
  RETURN
END`, "While,Return,EndStatement"},
		{"adjacent labels and statement boundaries", `CREATE PROCEDURE acme.p AS
  SET @name = 'x'
  goto Leave
Leave:
NextLabel:
  SELECT value FROM acme.items
LastLabel:`, "Set,Goto,Label,Label,Select,Label"},
		{"branches and error variable", `CREATE PROCEDURE acme.p AS BEGIN
  IF @@error <> 0 GOTO E_Fail ELSE RETURN
  WHILE 1 = 1 IF @i > 10 BREAK ELSE CONTINUE
E_Fail:
  RETURN
END`, "IfBlock,While,Label,Return,EndStatement"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tree, err := ParseOne(c.sql, "tsql")
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := ParseScript(c.sql, "tsql")
			if err != nil || !parsed.Complete || parsed.Tree == nil || parsed.Tree.Class != "Create" || len(parsed.Tree.FindAll("Create")) != 1 {
				t.Fatalf("script: %+v (%v)", parsed, err)
			}
			body := tree.Args["expression"].(*Expression).Args["expressions"].([]*Expression)
			var classes []string
			for _, statement := range body {
				classes = append(classes, statement.Class)
			}
			if got := strings.Join(classes, ","); got != c.classes {
				t.Fatalf("body is %s, want %s", got, c.classes)
			}
			if nodes := tree.FindAll("Command"); len(nodes) != 0 {
				t.Fatalf("opaque statements: %v", nodes)
			}
			written, err := Generate(tree, "tsql")
			if err != nil {
				t.Fatal(err)
			}
			again, err := ParseOne(written, "tsql")
			if err != nil {
				t.Fatalf("re-parse %q: %v", written, err)
			}
			if rewritten, err := Generate(again, "tsql"); err != nil || rewritten != written {
				t.Fatalf("not a fixed point: %q (%v), want %q", rewritten, err, written)
			}
		})
	}
}

func TestTSQLControlFlowNodes(t *testing.T) {
	tree, err := ParseOne("CREATE PROCEDURE acme.p AS BEGIN WHILE 1 = 1 BEGIN IF @i > 10 BREAK IF @i = 5 CONTINUE IF @i = 9 goto CleanUp END CleanUp: RETURN END", "tsql")
	if err != nil {
		t.Fatal(err)
	}
	var classes []string
	for _, branch := range tree.FindAll("IfBlock") {
		classes = append(classes, branch.Args["true"].(*Expression).Class)
	}
	if got := strings.Join(classes, ","); got != "Break,Continue,Goto" {
		t.Fatalf("IF branches are %s", got)
	}
	for _, class := range []string{"Goto", "Label"} {
		nodes := tree.FindAll(class)
		if len(nodes) != 1 || nodes[0].This() == nil || nodes[0].This().Class != "Identifier" || nodes[0].Name() != "CleanUp" {
			t.Fatalf("%s nodes: %v", class, nodes)
		}
	}
}

func TestTSQLControlFlowRefusals(t *testing.T) {
	for _, body := range []string{"GOTO;", "GOTO 5", ":", "5:", "GOTO @label", "GOTO 'label'", "BREAK 5", "CONTINUE @i", "FROBNICATE @x;", "frobnicate;"} {
		t.Run(body, func(t *testing.T) {
			sql := "CREATE PROCEDURE acme.p AS BEGIN " + body + " END"
			if tree, err := ParseOne(sql, "tsql"); err == nil {
				t.Fatalf("accepted as %s", tree.Class)
			}
			parsed, err := ParseScript(sql, "tsql")
			if err != nil || parsed.Tree != nil || len(parsed.Diagnostics) == 0 {
				t.Fatalf("script refusal: %+v (%v)", parsed, err)
			}
		})
	}
}
