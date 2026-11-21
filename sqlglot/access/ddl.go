package access

import (
	"strings"

	"github.com/flanksource/sqlglot-go/sqlglot"
)

// create records CREATE TABLE, VIEW or INDEX as a write of the object it
// creates, or of the table an index is on. A view stores its query, so what
// the query reads is the view's, not the statement's; a CREATE TABLE … AS
// query runs it. A table's foreign keys read the tables they reference.
func (r *reader) create(e *sqlglot.Expression) error {
	target := e.This()
	switch {
	case target == nil:
		return &UnsupportedSQLError{Class: e.Class}
	case target.Class == "Schema":
		if err := r.walkArgs(target, "this"); err != nil {
			return err
		}
		target = target.This()
	case target.Class == "Index":
		target = child(target, "table")
	}
	if err := r.writeTarget(e, target); err != nil {
		return err
	}
	if strings.EqualFold(ddlKind(e), "VIEW") {
		return nil
	}
	return r.walk(child(e, "expression"))
}

// alter records ALTER TABLE as a write of the table, and reads what its
// actions reference. An altered view stores its query, as a created one does.
func (r *reader) alter(e *sqlglot.Expression) error {
	if err := r.writeTarget(e, e.This()); err != nil {
		return err
	}
	if strings.EqualFold(ddlKind(e), "VIEW") {
		return nil
	}
	return r.walkKey(e, "actions")
}

// dropOrTruncate records DROP and TRUNCATE as a write of each object they
// name; key holds the names.
func (r *reader) dropOrTruncate(e *sqlglot.Expression, key string) error {
	targets := list(e, key)
	if len(targets) == 0 {
		return &UnsupportedSQLError{Class: e.Class}
	}
	for _, target := range targets {
		if err := r.writeTarget(e, target); err != nil {
			return err
		}
	}
	return nil
}

// writeTarget records the write of the Table node target a DDL statement e
// names; a temp table is none.
func (r *reader) writeTarget(e, target *sqlglot.Expression) error {
	if target == nil || target.Class != "Table" {
		return &UnsupportedSQLError{Class: e.Class + "(" + ddlKind(e) + ")"}
	}
	r.targets[target] = true
	return r.source(target)
}

func ddlKind(e *sqlglot.Expression) string {
	kind, _ := e.Args["kind"].(string)
	return kind
}
