package harness_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/flanksource/sqlglot-go/sqlglot"
)

// The synthetic T-SQL routine corpus: hand-written definitions over
// placeholder schemas, one file per construct plus a few that combine them,
// held to what a migration tool needs from the port -- every definition
// parses, writes back to a fixed point, keeps the statements it was written
// with, and canonicalises equal exactly when only spelling differs.

const routinesDir = "../testdata/tsql-routines"

// SQLGLOT_GO_UPDATE_GOLDEN=1 rewrites statements.golden from what the port
// reads now, for review in the diff.
const updateGolden = "SQLGLOT_GO_UPDATE_GOLDEN"

func TestTSQLRoutineCorpus(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(routinesDir, "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no routines in %s (%v)", routinesDir, err)
	}
	sort.Strings(files)
	var listing strings.Builder
	for _, file := range files {
		tree := parseRoutineFile(t, file)
		assertFixedPoint(t, file, tree)
		listing.WriteString(filepath.Base(file) + "\n" + statementListing(tree, "  "))
	}
	golden := filepath.Join(routinesDir, "statements.golden")
	if os.Getenv(updateGolden) != "" {
		if err := os.WriteFile(golden, []byte(listing.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := readCorpusFile(golden)
	if err != nil {
		t.Fatalf("%v (set %s=1 to write it)", err, updateGolden)
	}
	if listing.String() != string(want) {
		t.Errorf("statement classes differ from %s (set %s=1 and review the diff):\n%s", golden, updateGolden, listing.String())
	}
}

func readCorpusFile(file string) ([]byte, error) {
	return os.ReadFile(file) //nolint:gosec // G304: files globbed from the committed corpus directory
}

// parseRoutineFile reads a file as a script: exactly one batch, holding one
// routine.
func parseRoutineFile(t *testing.T, file string) *sqlglot.Expression {
	t.Helper()
	raw, err := readCorpusFile(file)
	if err != nil {
		t.Fatal(err)
	}
	batches, err := sqlglot.SplitBatches(string(raw), "tsql")
	if err != nil || len(batches) != 1 {
		t.Fatalf("%s: %d batches (%v)", file, len(batches), err)
	}
	tree, err := sqlglot.ParseOne(batches[0], "tsql")
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	if _, err := sqlglot.RoutineOf(tree); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return tree
}

func assertFixedPoint(t *testing.T, file string, tree *sqlglot.Expression) {
	t.Helper()
	written, err := sqlglot.Generate(tree, "tsql")
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	again, err := sqlglot.ParseOne(written, "tsql")
	if err != nil {
		t.Fatalf("%s: what the port wrote does not parse: %v\n%s", file, err, written)
	}
	if rewritten, err := sqlglot.Generate(again, "tsql"); err != nil || rewritten != written {
		t.Fatalf("%s: not a fixed point (%v)\n%s\n%s", file, err, written, rewritten)
	}
}

// statementListing names each statement of a routine body, one per line,
// with what an IF runs and what a nested block holds indented beneath it.
func statementListing(e *sqlglot.Expression, indent string) string {
	if e.Class == "Create" {
		kind, _ := e.Args["kind"].(string)
		out := indent + "Create " + kind + "\n"
		if body, _ := e.Args["expression"].(*sqlglot.Expression); body != nil {
			out += statementListing(body, indent+"  ")
		}
		return out
	}
	if e.Class != "Block" {
		return indent + e.Class + "\n" + branches(e, indent)
	}
	var out strings.Builder
	statements, _ := e.Args["expressions"].([]*sqlglot.Expression)
	for _, statement := range statements {
		if statement.Class == "Block" {
			out.WriteString(indent + "Block\n" + statementListing(statement, indent+"  "))
			continue
		}
		out.WriteString(statementListing(statement, indent))
	}
	return out.String()
}

func branches(e *sqlglot.Expression, indent string) string {
	if e.Class != "IfBlock" {
		return ""
	}
	var out strings.Builder
	for _, key := range []string{"true", "false"} {
		if branch, _ := e.Args[key].(*sqlglot.Expression); branch != nil {
			out.WriteString(indent + "  " + key + ":\n" + statementListing(branch, indent+"    "))
		}
	}
	return out.String()
}

func TestTSQLRoutineRefusals(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(routinesDir, "refuse", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no refusals (%v)", err)
	}
	for _, file := range files {
		raw, err := readCorpusFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if tree, err := sqlglot.ParseOne(string(raw), "tsql"); err == nil {
			t.Errorf("%s: read as a %s; it should be refused", file, tree.Class)
		}
	}
}

func TestTSQLRoutineCanonicalPairs(t *testing.T) {
	bases, err := filepath.Glob(filepath.Join(routinesDir, "canonical", "*.a.sql"))
	if err != nil || len(bases) == 0 {
		t.Fatalf("no canonical pairs (%v)", err)
	}
	for _, base := range bases {
		stem := strings.TrimSuffix(base, ".a.sql")
		same, sameErr := os.Stat(stem + ".b.sql")
		diff, diffErr := os.Stat(stem + ".diff.sql")
		if sameErr != nil && diffErr != nil {
			t.Errorf("%s has neither a .b.sql nor a .diff.sql", base)
		}
		if same != nil {
			if a, b := canonicalFile(t, base), canonicalFile(t, stem+".b.sql"); a != b {
				t.Errorf("%s: canonical forms differ:\n%s\n%s", filepath.Base(stem), a, b)
			}
		}
		if diff != nil {
			if a, d := canonicalFile(t, base), canonicalFile(t, stem+".diff.sql"); a == d {
				t.Errorf("%s: the .diff.sql canonicalised equal: %s", filepath.Base(stem), a)
			}
		}
	}
}

func canonicalFile(t *testing.T, file string) string {
	t.Helper()
	raw, err := readCorpusFile(file)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := sqlglot.CanonicalSQL(string(raw), "tsql")
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return canonical
}
