package access

import (
	"fmt"
	"strings"

	"github.com/flanksource/sqlglot-go/sqlglot"
)

type MigrationIssue struct {
	Range   sqlglot.SourceRange
	Message string
}

type MigrationAccess struct {
	Accesses []Access
	Issues   []MigrationIssue
	Dynamic  []MigrationSQL
}

// MigrationSQL is literal SQL executed at the location of its enclosing call.
type MigrationSQL struct {
	Text  string
	Range sqlglot.SourceRange
}

func ReadMigrationStatement(statement *sqlglot.Expression, options StatementOptions) MigrationAccess {
	r := &reader{ctx: statementContext{StatementOptions: options}, migration: true,
		ctes: map[string]bool{}, targets: map[*sqlglot.Expression]bool{}, sourced: map[*sqlglot.Expression]bool{}, seen: map[string]bool{}}
	r.ctx.dynamic = func(text string) ([]Node, error) {
		if r.location == nil {
			return nil, fmt.Errorf("dynamic SQL has no source location")
		}
		return []Node{{Text: text, sourceRange: *r.location}}, nil
	}
	r.migrationChild(statement)
	result := MigrationAccess{Accesses: append(r.writes, r.reads...), Issues: r.issues}
	for _, script := range r.dynamic {
		result.Dynamic = append(result.Dynamic, MigrationSQL{Text: script.Text, Range: script.sourceRange})
	}
	return result
}

func (r *reader) migrationChild(e *sqlglot.Expression) {
	if e == nil {
		return
	}
	if err := r.statement(e); err != nil {
		location, ok := e.SourceRange()
		if !ok && r.location != nil {
			location = *r.location
		}
		r.issues = append(r.issues, MigrationIssue{Range: location, Message: err.Error()})
	}
}

func (r *reader) migrationStatement(e *sqlglot.Expression) (bool, error) {
	switch e.Class {
	case "Block":
		for _, statement := range children(e) {
			r.migrationChild(statement)
		}
	case "IfBlock", "While":
		err := r.walk(e.This())
		for _, key := range []string{"true", "false", "expression"} {
			r.migrationChild(child(e, key))
		}
		return true, err
	case "TryCatch":
		r.migrationChild(child(e, "try"))
		r.migrationChild(child(e, "catch"))
	case "Create", "Alter", "Drop":
		return r.migrationDDL(e)
	case "Return", "Print", "Throw", "Raiserror", "Assignment", "SetItem", "Values", "SelectInto":
		return true, r.walkArgs(e)
	case "Do", "EndStatement", "Transaction", "Commit", "Rollback", "Use", "ExecuteAs", "Revert", "Break", "Continue", "Label", "Goto",
		"Grant", "Revoke", "Comment", "Pragma", "Attach", "Detach":
	default:
		return false, nil
	}
	return true, nil
}

func (r *reader) migrationDDL(e *sqlglot.Expression) (bool, error) {
	switch strings.ToUpper(ddlKind(e)) {
	case "FUNCTION", "PROCEDURE", "PROC":
		targets := list(e, "tables")
		if len(targets) == 0 {
			targets = []*sqlglot.Expression{e.This()}
		}
		for _, target := range targets {
			if target != nil && (target.Class == "UserDefinedFunction" || target.Class == "StoredProcedure") {
				target = target.This()
			}
			ref, ok := r.tableIdentity(target)
			if !ok {
				return true, &UnsupportedSQLError{Class: e.Class + " " + ddlKind(e)}
			}
			if strings.EqualFold(ddlKind(e), "FUNCTION") {
				r.writes = append(r.writes, Access{Kind: Write, Object: ref.object(r.ctx.DefaultSchema), ObjectKind: "function", Range: r.location})
			}
		}
		r.migrationBody(child(e, "expression"))
	case "VIEW":
		if err := r.writeTarget(e, e.This()); err != nil {
			return true, err
		}
		r.migrationBody(child(e, "expression"))
	case "TRIGGER":
		r.migrationBody(child(e, "expression"))
		props := child(e, "properties")
		for _, item := range children(props) {
			if item.Class == "TriggerProperties" {
				return true, r.walkArgs(item, "table")
			}
		}
		return true, r.walk(child(e, "when"))
	case "SCHEMA", "DATABASE", "NAMESPACE", "SEQUENCE", "TYPE", "EXTENSION":
		return true, nil
	default:
		return false, nil
	}
	return true, nil
}

func (r *reader) migrationBody(e *sqlglot.Expression) {
	if e != nil && e.Class != "Heredoc" && e.Class != "Literal" {
		r.migrationChild(e)
	}
}

func (r *reader) migrationCall(ref tableRef, e *sqlglot.Expression) {
	loc := r.location
	if span, ok := e.SourceRange(); ok {
		loc = &span
	}
	r.reads = append(r.reads, Access{Kind: Call, Object: ref.object(r.ctx.DefaultSchema), ObjectKind: "function", Range: loc})
}

func (r *reader) nameKey(name string) string {
	if r.migration && r.ctx.Dialect == "postgres" {
		return name
	}
	return strings.ToLower(name)
}
