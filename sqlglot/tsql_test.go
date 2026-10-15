package sqlglot

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitBatches(t *testing.T) {
	for _, c := range []struct{ name, script string }{
		{"GO on its own lines, any case, with comments around it", strings.Join([]string{
			"-- header",
			"CREATE PROCEDURE acme.a AS SELECT 1;",
			"GO",
			"",
			"CREATE PROCEDURE acme.b AS SELECT 'GO'; -- GO",
			"/*",
			"GO",
			"*/",
			"go -- trailing comment",
			"",
			"GO",
			"   ",
		}, "\n")},
	} {
		t.Run(c.name, func(t *testing.T) {
			batches, err := SplitBatches(c.script, "tsql")
			if err != nil {
				t.Fatal(err)
			}
			want := []string{
				"-- header\nCREATE PROCEDURE acme.a AS SELECT 1;",
				"CREATE PROCEDURE acme.b AS SELECT 'GO'; -- GO\n/*\nGO\n*/",
			}
			if !reflect.DeepEqual(batches, want) {
				t.Fatalf("split into %q", batches)
			}
		})
	}
	if batches, err := SplitBatches("SELECT 1 GO", "tsql"); err != nil || len(batches) != 1 {
		t.Fatalf("a GO that does not begin its line split the batch: %q (%v)", batches, err)
	}
	for _, script := range []string{"SELECT 1\nGO 5\nSELECT 2", "SELECT 1\nGO SELECT 2", "SELECT 'unterminated"} {
		if batches, err := SplitBatches(script, "tsql"); err == nil {
			t.Errorf("%q split into %q; it should be refused", script, batches)
		}
	}
}

func TestCanonicalSQL(t *testing.T) {
	base := "CREATE PROCEDURE acme.p @a INT = 1 OUTPUT AS BEGIN SELECT 'a  b' FROM acme.orders; END"
	for _, same := range []string{
		"create or alter proc [acme].[p] @a int = 1 output as begin -- c\n select 'a  b' from [acme].orders end",
		"CREATE   PROCEDURE acme.p\n  @a INT = 1 OUT AS BEGIN /* c */ SELECT 'a  b' FROM acme.orders; END",
	} {
		if !canonicallyEqual(t, base, same) {
			t.Errorf("%q did not canonicalise like %q", same, base)
		}
	}
	for _, different := range []string{
		"CREATE PROCEDURE acme.p @a INT = 1 OUTPUT AS BEGIN SELECT 'a b' FROM acme.orders; END",
		"CREATE PROCEDURE acme.p @a INT = 1 OUTPUT AS BEGIN SELECT 'a  b' FROM acme.orders; RETURN; END",
		"CREATE PROCEDURE acme.p @a INT = 1 READONLY AS BEGIN SELECT 'a  b' FROM acme.orders; END",
	} {
		if canonicallyEqual(t, base, different) {
			t.Errorf("%q canonicalised like %q", different, base)
		}
	}
	// A name that is a keyword keeps its brackets.
	if got, err := CanonicalSQL("SELECT [select] FROM [t]", "tsql"); err != nil || got != "SELECT [select] FROM t" {
		t.Errorf("wrote %q (%v)", got, err)
	}
	if _, err := CanonicalSQL("CREATE PROCEDURE acme.p AS BEGIN SELECT 1", "tsql"); err == nil {
		t.Error("an unparseable definition was canonicalised")
	}
}

func canonicallyEqual(t *testing.T, a, b string) bool {
	t.Helper()
	ca, err := CanonicalSQL(a, "tsql")
	if err != nil {
		t.Fatalf("%q: %v", a, err)
	}
	cb, err := CanonicalSQL(b, "tsql")
	if err != nil {
		t.Fatalf("%q: %v", b, err)
	}
	return ca == cb
}

func TestRoutineOf(t *testing.T) {
	tree, err := ParseOne("CREATE OR ALTER PROC acme.p @a NVARCHAR(MAX) = N'x' OUTPUT, @t acme.ids READONLY AS SELECT 1", "tsql")
	if err != nil {
		t.Fatal(err)
	}
	routine, err := RoutineOf(tree)
	if err != nil {
		t.Fatal(err)
	}
	got := []any{routine.Kind, routine.Schema, routine.Name, routine.OrAlter, len(routine.Params), routine.Returns == nil, routine.Body.Class}
	if want := []any{"PROCEDURE", "acme", "p", true, 2, true, "Block"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("routine is %v, want %v", got, want)
	}
	a, tvp := routine.Params[0], routine.Params[1]
	if a.Name != "@a" || typeKind(a.Type) != "NVARCHAR" || a.Default.Class != "National" || !a.Output || a.ReadOnly {
		t.Errorf("@a read as %+v", a)
	}
	if tvp.Name != "@t" || tvp.Default != nil || tvp.Output || !tvp.ReadOnly {
		t.Errorf("@t read as %+v", tvp)
	}

	fn, err := ParseOne("CREATE FUNCTION f(@a INT) RETURNS DECIMAL(9, 2) AS BEGIN RETURN @a END", "tsql")
	if err != nil {
		t.Fatal(err)
	}
	routine, err = RoutineOf(fn)
	if err != nil {
		t.Fatal(err)
	}
	if routine.Kind != "FUNCTION" || routine.Schema != "" || routine.Name != "f" || routine.OrAlter ||
		typeKind(routine.Returns) != "DECIMAL" || len(routine.Params) != 1 {
		t.Errorf("function read as %+v", routine)
	}

	for _, sql := range []string{"SELECT 1", "CREATE TABLE t (a INT)", "CREATE VIEW v AS SELECT 1"} {
		tree, err := ParseOne(sql, "tsql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := RoutineOf(tree); err == nil {
			t.Errorf("%q read as a routine", sql)
		}
	}
	if _, err := RoutineOf(nil); err == nil {
		t.Error("nil read as a routine")
	}
}

func TestParseDataType(t *testing.T) {
	n := func(v int) *int { return &v }
	for _, c := range []struct {
		in   string
		want DataTypeSpec
	}{
		{"nvarchar(max)", DataTypeSpec{Kind: "NVARCHAR", Name: "NVARCHAR", Max: true}},
		{"varchar(50)", DataTypeSpec{Kind: "VARCHAR", Name: "VARCHAR", Length: n(50)}},
		{"decimal(18,2)", DataTypeSpec{Kind: "DECIMAL", Name: "NUMERIC", Precision: n(18), Scale: n(2)}},
		{"datetime2(7)", DataTypeSpec{Kind: "DATETIME2", Name: "DATETIME2", Scale: n(7)}},
		{"uniqueidentifier", DataTypeSpec{Kind: "UUID", Name: "UNIQUEIDENTIFIER"}},
		{"varbinary(max)", DataTypeSpec{Kind: "VARBINARY", Name: "VARBINARY", Max: true}},
		{"float(53)", DataTypeSpec{Kind: "FLOAT", Name: "FLOAT", Precision: n(53)}},
		{"int", DataTypeSpec{Kind: "INT", Name: "INTEGER"}},
	} {
		t.Run(c.in, func(t *testing.T) {
			e, err := ParseDataType(c.in, "tsql")
			if err != nil {
				t.Fatal(err)
			}
			spec, err := DataTypeSpecOf(e, "tsql")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(spec, c.want) {
				t.Fatalf("read %s, want %s", spec, c.want)
			}
		})
	}
	for _, in := range []string{"", "varchar(50) x", "not a type (", "1"} {
		if e, err := ParseDataType(in, "tsql"); err == nil {
			t.Errorf("%q read as %v", in, e.Class)
		}
	}
	if _, err := DataTypeSpecOf(New("Column"), "tsql"); err == nil {
		t.Error("a Column read as a data type")
	}
}
