package scripts

import (
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/sqlglot-go/sqlglot"
)

type Analysis struct {
	Calls              []string                  `json:"calls"`
	Builtins           []sqlglot.BuiltinFunction `json:"builtins,omitempty"`
	Blocked            []string                  `json:"blocked,omitempty"`
	Unresolved         []string                  `json:"unresolved,omitempty"`
	TransactionControl bool                      `json:"transactionControl,omitempty"`
}

type AnalysisOptions struct {
	SQL           string
	InvokedModule bool
}

// Analyze visits executable expressions, leaving stored module bodies for catalog analysis.
func Analyze(opts AnalysisOptions) Analysis { return analyze(opts, 0) }

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
				a.Blocked = append(a.Blocked, nested.Blocked...)
				a.Unresolved = append(a.Unresolved, nested.Unresolved...)
				a.TransactionControl = a.TransactionControl || nested.TransactionControl
			} else if child != nil && child.Class == "Table" {
				db, _ := child.Args["db"].(*sqlglot.Expression)
				if db == nil {
					a.Calls = append(a.Calls, "dbo."+child.Name())
				} else {
					a.Calls = append(a.Calls, db.Name()+"."+child.Name())
				}
			} else {
				a.Blocked = append(a.Blocked, "EXEC requires explicit runtime dependencies")
			}
		}
		return true
	})
	slices.Sort(a.Calls)
	a.Calls = slices.Compact(a.Calls)
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
