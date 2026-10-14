package sqlglot

import (
	"testing"
)

func TestFormat(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			name: "clauses break onto their own lines and select items one per line",
			in:   "select a, b,c from t where x = 1 and y = 2 order by a",
			want: "select a,\n  b,\n  c\nfrom t\nwhere x = 1\n  and y = 2\norder by a",
		},
		{
			name: "whitespace runs and line breaks collapse, keyword case is kept",
			in:   "SELECT  a\n\t FROM   t\n WHERE x=1",
			want: "SELECT a\nFROM t\nWHERE x=1",
		},
		{
			name: "joins start a line and ON is indented under them",
			in:   "SELECT a FROM t LEFT OUTER JOIN u ON u.id = t.id AND u.k = 1 JOIN v ON v.id = t.id",
			want: "SELECT a\nFROM t\nLEFT OUTER JOIN u\n  ON u.id = t.id\n  AND u.k = 1\nJOIN v\n  ON v.id = t.id",
		},
		{
			name: "LEFT and RIGHT as functions stay inline",
			in:   "SELECT LEFT(a, 3), RIGHT(b,2) FROM t",
			want: "SELECT LEFT(a, 3),\n  RIGHT(b,2)\nFROM t",
		},
		{
			name: "a subquery is indented and its closing paren returns to the outer level",
			in:   "SELECT a FROM t WHERE t.id IN (SELECT id FROM u WHERE k = 1) AND z = 2",
			want: "SELECT a\nFROM t\nWHERE t.id IN (\n  SELECT id\n  FROM u\n  WHERE k = 1\n)\n  AND z = 2",
		},
		{
			name: "AND inside BETWEEN, CASE and plain parentheses stays inline",
			in:   "SELECT CASE WHEN a = 1 AND b = 2 THEN 1 END FROM t WHERE d BETWEEN 1 AND 2 AND (p = 1 OR q = 2)",
			want: "SELECT CASE WHEN a = 1 AND b = 2 THEN 1 END\nFROM t\nWHERE d BETWEEN 1 AND 2\n  AND (p = 1 OR q = 2)",
		},
		{
			name: "OIPA placeholders keep their text",
			in:   "SELECT FieldValue FROM AsPolicyField WHERE PolicyGUID = '[PolicyGUID]' AND FieldName = [Activity:FieldName]",
			want: "SELECT FieldValue\nFROM AsPolicyField\nWHERE PolicyGUID = '[PolicyGUID]'\n  AND FieldName = [Activity:FieldName]",
		},
		{
			name: "string literals keep their inner whitespace",
			in:   "SELECT 'a   b' FROM t",
			want: "SELECT 'a   b'\nFROM t",
		},
		{
			name: "comments are kept and a line comment ends its line",
			in:   "SELECT a -- the a\nFROM t /* only t */ WHERE x = 1",
			want: "SELECT a -- the a\nFROM t /* only t */\nWHERE x = 1",
		},
		{
			name: "group by and union",
			in:   "SELECT a FROM t GROUP  BY a UNION ALL SELECT b FROM u",
			want: "SELECT a\nFROM t\nGROUP BY a\nUNION ALL\nSELECT b\nFROM u",
		},
		{
			name: "statements separated by a semicolon start at the left edge",
			in:   "UPDATE t SET a = 1, b = 2 WHERE id = 3; DELETE FROM u",
			want: "UPDATE t\nSET a = 1,\n  b = 2\nWHERE id = 3;\nDELETE FROM u",
		},
		{
			name: "empty input formats to empty",
			in:   "  \n ",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Format(tc.in, "tsql")
			if err != nil {
				t.Fatalf("Format(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("Format(%q)\n got: %q\nwant: %q", tc.in, got, tc.want)
			}
			again, err := Format(got, "tsql")
			if err != nil {
				t.Fatalf("Format of formatted output %q: %v", got, err)
			}
			if again != got {
				t.Fatalf("Format is not idempotent\nonce:  %q\ntwice: %q", got, again)
			}
		})
	}
}

func TestFormatRejectsWhatItCannotLex(t *testing.T) {
	if _, err := Format("SELECT 'unterminated", "tsql"); err == nil {
		t.Fatal("Format accepted an unterminated string")
	}
	if _, err := Format("SELECT 1", "no-such-dialect"); err == nil {
		t.Fatal("Format accepted an unknown dialect")
	}
}
