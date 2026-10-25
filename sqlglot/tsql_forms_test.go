package sqlglot

import (
	"strings"
	"testing"
)

func TestTSQLForms(t *testing.T) {
	for _, c := range []struct{ name, sql, classes string }{
		{"cursor", `CREATE PROCEDURE acme.lock_entities AS
BEGIN
 DECLARE @c CURSOR, @id NVARCHAR(36)
 SET @c = CURSOR LOCAL FAST_FORWARD FOR
 SELECT CONVERT(NVARCHAR(36), e.entity_id) FROM acme.entity_lock e WHERE e.owner_id IS NOT NULL
 OPEN @c
 FETCH NEXT FROM @c INTO @id
 WHILE @@FETCH_STATUS = 0
 BEGIN
 FETCH NEXT FROM @c INTO @id
 END
 CLOSE @c
 DEALLOCATE @c
END`, "Declare,SetCursor,OpenCursor,FetchCursor,While,CloseCursor,DeallocateCursor,EndStatement"},
		{"statistics", `CREATE PROCEDURE acme.refresh_cycle AS
BEGIN
 UPDATE acme.cycle SET status = 1
 BEGIN
 UPDATE STATISTICS acme_cycle
 END
 UPDATE STATISTICS acme.cycle IX_cycle_status WITH FULLSCAN
END`, "Update,Block,UpdateStatistics,EndStatement"},
		{"constraint", `CREATE PROCEDURE acme.upgrade_items AS
BEGIN
 CREATE TABLE acme.items (
 name SYSNAME NOT NULL,
 owner_id INT NOT NULL, -- may become varbinary(85)
 item_id INT PRIMARY KEY IDENTITY,
 version INT,
 definition VARBINARY(MAX)
 CONSTRAINT UK_owner_name UNIQUE (owner_id, name)
 );
END`, "Create,EndStatement"},
		{"xml", `CREATE PROCEDURE acme.read_fields @doc XML AS
BEGIN
 DECLARE @fields TABLE (name NVARCHAR(50), kind NVARCHAR(30))
 INSERT INTO @fields (name, kind)
 SELECT f.n.value('(Name/text())[1]', 'nvarchar(50)'),
 LOWER(LTRIM(RTRIM(f.n.value('(DataType/text())[1]', 'nvarchar(30)'))))
 FROM @doc.nodes('/Transaction/Fields/Field') AS f(n);
 SELECT x.n.value('@id', 'int') FROM acme.docs d CROSS APPLY d.payload.nodes('/Root/Item') AS x(n)
END`, "Declare,Insert,Select,EndStatement"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tree := assertTSQLFormsRoundTrip(t, c.sql, "Create")
			var classes []string
			for _, statement := range tree.Args["expression"].(*Expression).Args["expressions"].([]*Expression) {
				classes = append(classes, statement.Class)
			}
			if got := strings.Join(classes, ","); got != c.classes {
				t.Fatalf("body is %s, want %s", got, c.classes)
			}
			if c.name == "constraint" && len(tree.FindAll("Constraint")) != 1 {
				t.Fatal("missing table Constraint")
			}
			if c.name == "constraint" {
				written, err := Generate(tree, "tsql")
				if err != nil || !strings.Contains(written, ", CONSTRAINT UK_owner_name UNIQUE") {
					t.Fatalf("table constraint has no canonical comma: %s (%v)", written, err)
				}
			}
			if c.name == "xml" {
				counts := map[string]int{}
				for _, call := range tree.FindAll("Anonymous") {
					if name, _ := call.Args["this"].(string); name == "nodes" || name == "value" {
						if call.Parent == nil || call.Parent.Class != "Dot" {
							t.Fatalf("XML %s is not a method call", name)
						}
						counts[name]++
					}
				}
				if counts["nodes"] != 2 || counts["value"] != 3 {
					t.Fatalf("XML method calls: %v", counts)
				}
			}
		})
	}
}

func assertTSQLFormsRoundTrip(t *testing.T, sql, class string) *Expression {
	t.Helper()
	tree, err := ParseOne(sql, "tsql")
	if err != nil {
		t.Fatal(err)
	}
	if tree.Class != class {
		t.Fatalf("read %s, want %s", tree.Class, class)
	}
	script, err := ParseScript(sql, "tsql")
	if err != nil || !script.Complete || len(script.Diagnostics) != 0 || script.Tree == nil || script.Tree.Class != class {
		t.Fatalf("script: %+v (%v)", script, err)
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
		t.Fatalf("unstable: %s (%v), want %s", rewritten, err, written)
	}
	return tree
}

func TestTSQLFormsStandalone(t *testing.T) {
	for _, c := range []struct{ sql, class string }{
		{"SET @c = CURSOR GLOBAL SCROLL KEYSET OPTIMISTIC FOR SELECT id FROM acme.items FOR UPDATE OF id", "SetCursor"},
		{"SET @c = CURSOR FOR SELECT id FROM acme.items FOR UPDATE", "SetCursor"},
		{"CREATE TABLE acme.items (id INT, name NVARCHAR(50) CONSTRAINT UK_name UNIQUE (name))", "Create"},
		{"CREATE TABLE acme.items (id INT CONSTRAINT PK_items PRIMARY KEY)", "Create"},
		{"CREATE TABLE acme.items (id INT, doubled AS (id * 2) CONSTRAINT UK_doubled UNIQUE (doubled))", "Create"},
		{"CREATE TABLE acme.items (id INT UNIQUE (id))", "Create"},
		{"SELECT f.n.value('@id', 'int') FROM @doc.nodes('/Root/Item') AS f(n)", "Select"},
		{"SELECT f.n.value('@id', 'int') FROM acme.docs d CROSS APPLY d.payload.nodes('/Root/Item') AS f(n)", "Select"},
		{"SELECT f.n.value('@id', 'int') FROM acme.docs d CROSS APPLY @doc.nodes('/Root/Item') AS f(n)", "Select"},
		{"SELECT f.n.value('@id', 'int') FROM acme.docs d OUTER APPLY d.payload.nodes('/Root/Item') AS f(n)", "Select"},
		{"SELECT f.n.value('@id', 'int') FROM payload.nodes('/Root/Item') AS f(n)", "Select"},
	} {
		t.Run(c.sql, func(t *testing.T) { assertTSQLFormsRoundTrip(t, c.sql, c.class) })
	}
}

// A routine body refuses raw-text Commands, so UPDATE STATISTICS reads there
// as a typed statement; at top level it stays the reference's Command.
func TestTSQLUpdateStatisticsInRoutineBody(t *testing.T) {
	for _, statement := range []string{
		"UPDATE STATISTICS acme.items",
		"UPDATE STATISTICS acme.items (IX_name, IX_owner) WITH SAMPLE 25 PERCENT, NORECOMPUTE",
		"UPDATE STATISTICS acme.items WITH RESAMPLE",
		"UPDATE STATISTICS acme.items WITH SAMPLE 1000 ROWS, INDEX, NORECOMPUTE, MAXDOP = 2",
		"UPDATE STATISTICS acme.items WITH FULLSCAN, COLUMNS, INCREMENTAL = ON, PERSIST_SAMPLE_PERCENT = OFF, AUTO_DROP = ON",
	} {
		t.Run(statement, func(t *testing.T) {
			tree := assertTSQLFormsRoundTrip(t, "CREATE PROCEDURE acme.p AS BEGIN "+statement+" END", "Create")
			if found := tree.FindAll("UpdateStatistics"); len(found) != 1 {
				t.Fatalf("body holds %d UpdateStatistics statements, want 1", len(found))
			}
		})
	}
}

func TestTSQLFormsRefusals(t *testing.T) {
	for _, sql := range []string{
		"SET @c = CURSOR FOR",
		"SET @c = CURSOR LOCAL FOR SELECT id FROM acme.items FOR UPDATE OF",
		"SELECT * FROM @doc.nodes() AS f(n)",
		"SELECT * FROM @doc.nodes('/Root', '/Other') AS f(n)",
		"SELECT * FROM acme.docs d CROSS APPLY d.payload.nodes() AS f(n)",
	} {
		for _, input := range []string{sql, "CREATE PROCEDURE acme.p AS BEGIN " + sql + "; END"} {
			t.Run(input, func(t *testing.T) {
				if _, err := ParseOne(input, "tsql"); err == nil {
					t.Fatal("accepted malformed form")
				}
				if script, err := ParseScript(input, "tsql"); err == nil && script.Complete && len(script.Diagnostics) == 0 {
					t.Fatal("script accepted malformed form")
				}
			})
		}
	}
	for _, statement := range []string{
		"UPDATE STATISTICS",
		"UPDATE STATISTICS acme.items ()",
		"UPDATE STATISTICS acme.items WITH SAMPLE PERCENT",
		"UPDATE STATISTICS acme.items WITH",
	} {
		input := "CREATE PROCEDURE acme.p AS BEGIN " + statement + "; END"
		t.Run(input, func(t *testing.T) {
			if _, err := ParseOne(input, "tsql"); err == nil {
				t.Fatal("accepted malformed form")
			}
		})
	}
}
