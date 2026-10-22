package migrate

import "github.com/flanksource/sqlglot-go/sqlglot"

type ScriptAnalysis struct {
	Path        string                    `json:"path,omitempty"`
	Phase       string                    `json:"phase,omitempty"`
	Kind        string                    `json:"kind"`
	Complete    bool                      `json:"complete"`
	Diagnostics []sqlglot.ParseDiagnostic `json:"diagnostics,omitempty"`
	Error       string                    `json:"error,omitempty"`
}

func AnalyzeScript(text, dialect string) ScriptAnalysis {
	if dialect == "sqlserver" {
		dialect = "tsql"
	}
	result, err := sqlglot.ParseScript(text, dialect)
	a := ScriptAnalysis{Kind: "unknown", Complete: result.Complete, Diagnostics: result.Diagnostics}
	if err != nil {
		a.Error = err.Error()
		return a
	}
	if !result.Complete || len(result.Diagnostics) > 0 || result.Tree == nil {
		return a
	}
	ddl, dml, unknown := false, false, false
	result.Tree.Walk(func(e *sqlglot.Expression) bool {
		switch e.Class {
		case "Block":
			return true
		case "Create", "Alter", "Drop", "TruncateTable", "Grant", "Revoke":
			ddl = true
		case "Insert", "Update", "Delete", "Merge", "Select", "Union", "Except", "Intersect":
			dml = true
		default:
			unknown = true
		}
		return false
	})
	switch {
	case unknown:
		a.Kind = "unknown"
	case ddl && dml:
		a.Kind = "mixed"
	case ddl:
		a.Kind = "ddl"
	case dml:
		a.Kind = "dml"
	}
	return a
}
