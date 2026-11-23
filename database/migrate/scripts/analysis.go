package scripts

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/flanksource/sqlglot-go/sqlglot"
)

type Analysis struct {
	Calls              []string                  `json:"calls"`
	Builtins           []sqlglot.BuiltinFunction `json:"builtins,omitempty"`
	Blocked            []string                  `json:"blocked,omitempty"`
	Unresolved         []string                  `json:"unresolved,omitempty"`
	TransactionControl bool                      `json:"transactionControl,omitempty"`
	// Dynamic reports SQL known only at run time, whose calls analysis cannot list.
	Dynamic bool `json:"dynamic,omitempty"`
	// Environment are the calls whose value depends only on the target, as SQL:
	// what is tracked is their value, not that of the built-in inside.
	Environment []string `json:"environment,omitempty"`
	// Reads are the queries answering what running the body reads, and
	// UntrackedReads the tables it reads in no such query (see analyzeReads).
	Reads          []Read       `json:"reads,omitempty"`
	UntrackedReads []ObjectName `json:"untrackedReads,omitempty"`
	// Effects is what running the body does (see analyzeEffects); Defines
	// holds the body effects of each routine a script creates or alters.
	Effects Effects                `json:"effects"`
	Defines map[ObjectName]Effects `json:"defines,omitempty"`
	// Tails is what may follow each batch of a script body in its request.
	Tails        []sqlglot.BatchTail `json:"tails,omitempty"`
	NoStatements bool                `json:"noStatements,omitempty"`
}

type AnalysisOptions struct {
	SQL           string
	InvokedModule bool
}

// AnalysisVersion versions every cached analysis. Bump it whenever the
// analyser, the batch splitter or canonicalisation changes what it produces.
const AnalysisVersion = "8"

// CachedAnalysis is what a backend stores for one body: its analysis, the
// canonical text of a module definition, and the batch error of a script body
// that does not split.
type CachedAnalysis struct {
	Analysis   Analysis `json:"analysis"`
	Canonical  string   `json:"canonical,omitempty"`
	BatchError string   `json:"batchError,omitempty"`
}

// AnalysisKey is the content-addressed cache key of a body's analysis.
func AnalysisKey(sql string, invokedModule bool) string {
	return "sqlglot:analysis:" + AnalysisVersion + ":" + Digest(strconv.FormatBool(invokedModule), sql)
}

// Analyze visits executable expressions, leaving stored module bodies for
// catalog analysis, and reads the body's effects.
func Analyze(opts AnalysisOptions) Analysis {
	a := analyze(opts, 0)
	a.Effects, a.Defines = analyzeEffects(opts.SQL, opts.InvokedModule)
	if !opts.InvokedModule {
		a.Reads, a.UntrackedReads = analyzeReads(opts.SQL)
	}
	return a
}

func analyze(opts AnalysisOptions, depth int) Analysis {
	a := Analysis{Calls: []string{}}
	if depth > 16 {
		a.Blocked = []string{"dynamic SQL nesting exceeds 16"}
		return a
	}
	parsed, err := sqlglot.ParseScript(opts.SQL, "tsql")
	if err != nil {
		a.Blocked = []string{err.Error()}
		return a
	}
	if !parsed.Complete || len(parsed.Diagnostics) > 0 || parsed.Tree == nil {
		for _, d := range parsed.Diagnostics {
			a.Blocked = append(a.Blocked, d.Label)
		}
		if len(a.Blocked) == 0 {
			a.Blocked = append(a.Blocked, "incomplete script analysis")
		}
		return a
	}
	parsed.Tree.Walk(func(e *sqlglot.Expression) bool {
		if e.Class == "Create" && !opts.InvokedModule {
			if kind, _ := e.Args["kind"].(string); kind == "PROCEDURE" || kind == "FUNCTION" {
				return false
			}
		}
		if builtin, ok := e.Builtin("tsql"); ok {
			a.Builtins = append(a.Builtins, builtin)
			if slices.Contains(environmentBuiltins, builtin.Name) {
				expression, err := environmentExpression(e)
				if err != nil {
					a.Blocked = append(a.Blocked, err.Error())
				} else if expression != "" {
					a.Environment = append(a.Environment, expression)
				}
			}
		} else if e.Class == "Parameter" && e.This() != nil && e.This().Class == "Parameter" && e.This().This() != nil && e.This().This().Class == "Var" {
			a.Unresolved = append(a.Unresolved, "@@"+e.This().This().Name())
		}
		switch e.Class {
		case "Transaction", "Commit", "Rollback":
			a.TransactionControl = true
		}
		if e.Class == "Anonymous" {
			name := e.Name()
			if e.Parent != nil && e.Parent.Class == "Dot" {
				name = qualified(e.Parent)
			}
			if strings.Contains(name, ".") {
				a.Calls = append(a.Calls, name)
			} else if _, stateful := e.Builtin("tsql"); !stateful && !pureStringBuiltin(name) {
				a.Unresolved = append(a.Unresolved, name)
			}
		}
		if e.Class == "Execute" || e.Class == "ExecuteSql" {
			child := e.This()
			if e.Class == "ExecuteSql" {
				args, _ := e.Args["expressions"].([]*sqlglot.Expression)
				if len(args) > 0 {
					child = args[0]
				}
			}
			if child != nil && child.Class == "Paren" {
				child = child.This()
			}
			if child != nil && (child.Class == "Literal" || child.Class == "National") {
				nested := analyze(AnalysisOptions{SQL: child.Name(), InvokedModule: opts.InvokedModule}, depth+1)
				a.Calls = append(a.Calls, nested.Calls...)
				a.Builtins = append(a.Builtins, nested.Builtins...)
				a.Environment = append(a.Environment, nested.Environment...)
				a.Blocked = append(a.Blocked, nested.Blocked...)
				a.Unresolved = append(a.Unresolved, nested.Unresolved...)
				a.TransactionControl = a.TransactionControl || nested.TransactionControl
				a.Dynamic = a.Dynamic || nested.Dynamic
			} else if child != nil && child.Class == "Table" {
				db, _ := child.Args["db"].(*sqlglot.Expression)
				if db == nil {
					a.Calls = append(a.Calls, "dbo."+child.Name())
				} else {
					a.Calls = append(a.Calls, db.Name()+"."+child.Name())
				}
			} else {
				a.Dynamic = true
			}
		}
		return true
	})
	slices.Sort(a.Calls)
	a.Calls = slices.Compact(a.Calls)
	slices.Sort(a.Environment)
	a.Environment = slices.Compact(a.Environment)
	slices.Sort(a.Unresolved)
	a.Unresolved = slices.Compact(a.Unresolved)
	return a
}

func pureStringBuiltin(name string) bool {
	switch strings.ToUpper(name) {
	case "ABS", "CEILING", "FLOOR", "ROUND", "SIGN", "ISNULL", "COALESCE", "NULLIF", "LTRIM", "RTRIM", "TRIM", "LOWER", "UPPER", "LEFT", "RIGHT", "LEN", "DATALENGTH", "SUBSTRING", "REPLACE", "REVERSE", "CONCAT", "CONCAT_WS", "CHARINDEX", "IIF", "CAST", "CONVERT", "TRY_CAST", "TRY_CONVERT":
		return true
	default:
		return false
	}
}

func qualified(e *sqlglot.Expression) string {
	if e == nil {
		return ""
	}
	if e.Class == "Dot" {
		right, _ := e.Args["expression"].(*sqlglot.Expression)
		return qualified(e.This()) + "." + qualified(right)
	}
	return e.Name()
}

func QuoteName(name string) (string, error) {
	parts := strings.Split(name, ".")
	if len(parts) != 2 {
		return "", fmt.Errorf("expected schema-qualified local name, got %q", name)
	}
	for i, part := range parts {
		if strings.TrimSpace(part) == "" || strings.ContainsAny(part, "\x00\r\n") {
			return "", fmt.Errorf("invalid identifier %q", name)
		}
		parts[i] = "[" + strings.ReplaceAll(part, "]", "]]") + "]"
	}
	return strings.Join(parts, "."), nil
}
