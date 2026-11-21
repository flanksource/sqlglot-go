package access

import (
	"strings"

	"github.com/flanksource/sqlglot-go/sqlglot"
)

// source registers a FROM item (and the joins sqlglot hangs off it),
// recording the read -- or the write, for a DML target -- of a real table.
func (r *reader) source(item *sqlglot.Expression) error {
	if item == nil {
		return nil
	}
	r.sourced[item] = true
	switch item.Class {
	case "Table":
		if err := r.tableSource(item); err != nil {
			return err
		}
		for _, join := range list(item, "joins") {
			if err := r.source(join.This()); err != nil {
				return err
			}
		}
		return nil
	case "Subquery", "Lateral":
		return r.query(item.This())
	case "Values", "Unnest":
		return r.walkArgs(item, "alias")
	}
	return &UnsupportedSQLError{Class: item.Class}
}

// tableSource handles a Table FROM item: a CTE reference, a local table
// (table variable or #temp table), a table-valued function call, or a real
// table, which is the only one that is data access.
func (r *reader) tableSource(item *sqlglot.Expression) error {
	this := item.This()
	switch {
	case this == nil:
		return &UnsupportedSQLError{Class: "Table"}
	case this.Class == "Parameter", isTemporary(this):
		return nil
	case this.Class == "Anonymous":
		return r.functionSource(item)
	}
	ref, ok := tableRefOf(item)
	if !ok {
		return &UnsupportedSQLError{Class: "Table(" + this.Class + ")"}
	}
	if r.ctes[strings.ToLower(ref.name)] && ref.schema == "" && ref.catalog == "" {
		return nil
	}
	kind := Read
	if r.targets[item] {
		kind = Write
	}
	r.table(kind, ref)
	return nil
}

func isTemporary(name *sqlglot.Expression) bool {
	temporary, _ := name.Args["temporary"].(bool)
	return temporary || strings.HasPrefix(name.Name(), "#")
}

// functionSource records a table-valued function in FROM as a call.
func (r *reader) functionSource(item *sqlglot.Expression) error {
	fn := item.This()
	r.call(tableRef{catalog: child(item, "catalog").Name(), schema: child(item, "db").Name(), name: fn.Name()})
	return r.walkArgs(fn)
}

// tableRefOf reads the name parts of a Table node whose name is an
// identifier: `t`, `s.t`, `db.s.t`, or the four-part `srv.db.s.t` sqlglot
// keeps as a Dot.
func tableRefOf(table *sqlglot.Expression) (tableRef, bool) {
	ref := tableRef{catalog: child(table, "catalog").Name(), schema: child(table, "db").Name()}
	this := table.This()
	switch {
	case this == nil:
		return ref, false
	case this.Class == "Identifier":
		ref.name = this.Name()
	case this.Class == "Dot":
		ref.catalog, ref.schema, ref.name = ref.schema, this.Name(), child(this, "expression").Name()
	default:
		return ref, false
	}
	return ref, ref.name != ""
}

// aliasOf is the name a FROM item is referred to by: its alias, or else its
// own name.
func aliasOf(item *sqlglot.Expression) string {
	if alias := child(item, "alias"); alias != nil {
		return alias.Name()
	}
	if ref, ok := tableRefOf(item); ok {
		return ref.name
	}
	return ""
}

// typeMethods are the methods of SQL Server's xml type. `XMLData.value(…)`
// parses exactly like a schema-qualified call `dbo.fn(…)`; a method of a value
// is not a user-defined function.
var typeMethods = map[string]bool{"value": true, "query": true, "exist": true, "nodes": true, "modify": true}

// dotCall handles a Dot whose right side is a function call. A call
// qualified by one or two plain names (`dbo.fn(…)`, `db.dbo.fn(…)`) is a
// user-defined function; anything else is a method of a value, whose
// receiver and arguments are walked as ordinary expressions.
func (r *reader) dotCall(dot, fn *sqlglot.Expression) error {
	parts, ok := qualifierParts(dot.This())
	if ok && len(parts) <= 2 && !typeMethods[strings.ToLower(fn.Name())] {
		ref := tableRef{schema: parts[len(parts)-1], name: fn.Name()}
		if len(parts) == 2 {
			ref.catalog = parts[0]
		}
		return r.function(ref, fn)
	}
	if err := r.walk(dot.This()); err != nil {
		return err
	}
	return r.walkArgs(fn)
}

// unqualifiedCall handles a call by a bare name. SQL Server resolves a scalar
// user-defined function only by its schema-qualified name, so in T-SQL a bare
// name is a builtin sqlglot keeps untyped (DB_NAME, ISNUMERIC, QUOTENAME, …)
// and only its arguments are walked. PostgreSQL resolves functions by bare
// name, so there it is a call.
func (r *reader) unqualifiedCall(fn *sqlglot.Expression) error {
	if r.ctx.Dialect == "tsql" {
		return r.walkArgs(fn)
	}
	return r.function(tableRef{name: fn.Name()}, fn)
}

// qualifierParts reads `a` or `a.b` as its names; ok is false for anything
// that is not a chain of plain identifiers.
func qualifierParts(e *sqlglot.Expression) ([]string, bool) {
	switch {
	case e == nil:
		return nil, false
	case e.Class == "Identifier":
		return []string{e.Name()}, e.Name() != ""
	case e.Class == "Dot":
		left, ok := qualifierParts(e.This())
		if !ok {
			return nil, false
		}
		right, ok := qualifierParts(child(e, "expression"))
		return append(left, right...), ok
	}
	return nil, false
}

// function records a call to a function sqlglot does not model as a builtin
// (an Anonymous node), then walks its arguments for the calls they hold.
func (r *reader) function(ref tableRef, fn *sqlglot.Expression) error {
	if ref.name == "" {
		return &UnsupportedSQLError{Class: "Anonymous(unnamed)"}
	}
	r.call(ref)
	return r.walkArgs(fn)
}
