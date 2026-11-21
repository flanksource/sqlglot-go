package access

import (
	"slices"
	"strings"

	"github.com/flanksource/sqlglot-go/sqlglot"
)

// walk visits an expression. A Table that is not a registered FROM item is a
// read on its own; a SELECT … INTO table writes it.
func (r *reader) walk(e *sqlglot.Expression) error {
	if e == nil {
		return nil
	}
	switch e.Class {
	case "Select", "Union", "Intersect", "Except":
		return r.query(e)
	case "Column":
		return nil
	case "Command":
		return &UnsupportedSQLError{Class: e.Class}
	case "Anonymous":
		return r.unqualifiedCall(e)
	case "Dot":
		if fn := child(e, "expression"); fn != nil && fn.Class == "Anonymous" {
			return r.dotCall(e, fn)
		}
	case "Subquery":
		if r.sourced[e] {
			return nil
		}
	case "Table":
		if r.sourced[e] {
			return r.walkArgs(e, "this", "alias", "db", "catalog")
		}
		return r.source(e)
	case "Into":
		return r.into(e)
	}
	return r.walkArgs(e)
}

// into is a SELECT … INTO: a table it creates, or, in plpgsql, the
// variables it assigns.
func (r *reader) into(e *sqlglot.Expression) error {
	if r.ctx.IntoVariables {
		return nil
	}
	if table := e.This(); table != nil && table.Class == "Table" {
		r.targets[table] = true
		return r.source(table)
	}
	return r.walkArgs(e)
}

func (r *reader) walkArgs(e *sqlglot.Expression, skip ...string) error {
	for _, key := range e.Keys {
		if slices.Contains(skip, key) {
			continue
		}
		if err := r.walkKey(e, key); err != nil {
			return err
		}
	}
	return nil
}

func (r *reader) walkKey(e *sqlglot.Expression, key string) error {
	switch v := e.Args[key].(type) {
	case *sqlglot.Expression:
		return r.walk(v)
	case []*sqlglot.Expression:
		for _, item := range v {
			if err := r.walk(item); err != nil {
				return err
			}
		}
	}
	return nil
}

// query walks a SELECT or set operation: its CTEs, then its sources, then
// everything else it holds.
func (r *reader) query(e *sqlglot.Expression) error {
	switch e.Class {
	case "Select":
		return r.selectQuery(e)
	case "Subquery":
		return r.query(e.This())
	case "Union", "Intersect", "Except":
		if err := r.with(e); err != nil {
			return err
		}
		for _, arm := range []string{"this", "expression"} {
			if err := r.query(child(e, arm)); err != nil {
				return err
			}
		}
		return r.walkArgs(e, "this", "expression", "with_")
	}
	return r.walk(e)
}

func (r *reader) selectQuery(e *sqlglot.Expression) error {
	if err := r.with(e); err != nil {
		return err
	}
	if from := child(e, "from_"); from != nil {
		if err := r.source(from.This()); err != nil {
			return err
		}
	}
	for _, join := range list(e, "joins") {
		if err := r.source(join.This()); err != nil {
			return err
		}
	}
	return r.walkArgs(e, "with_")
}

// with registers each CTE of a statement by name, after walking its query,
// so a later FROM of that name resolves to the CTE and never to a table.
func (r *reader) with(e *sqlglot.Expression) error {
	for _, cte := range children(child(e, "with_")) {
		if err := r.query(cte.This()); err != nil {
			return err
		}
		r.ctes[strings.ToLower(child(cte, "alias").Name())] = true
	}
	return nil
}
