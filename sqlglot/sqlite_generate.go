package sqlglot

import (
	"fmt"
	"strings"
)

// sqliteNode writes the nodes SQLite's own generator overrides, where the
// generated spellings cannot say it. ok is false where the generic writers
// apply.
func (g *generator) sqliteNode(e *Expression) (string, bool) {
	switch e.Class {
	case "ComputedColumnConstraint":
		return g.sqliteComputedColumn(e), true
	case "JSONExtractScalar":
		if e.Args["json_subtype"] != true {
			return "", false
		}
		return "JSON_EXTRACT(" + g.child(e, "this") + ", " + g.child(e, "expression") + ")", true
	case "JSONExtract":
		rest, _ := e.Args["expressions"].([]*Expression)
		if len(rest) == 0 {
			return "", false
		}
		args := []string{g.child(e, "this"), g.child(e, "expression")}
		for _, r := range rest {
			args = append(args, g.node(r))
		}
		return "JSON_EXTRACT(" + strings.Join(args, ", ") + ")", true
	case "GroupConcat":
		return g.sqliteGroupConcat(e), true
	case "Transaction":
		return g.sqliteTransaction(e)
	case "WindowSpec":
		kind, _ := e.Args["kind"].(string)
		start, _ := e.Args["start"].(string)
		if strings.EqualFold(kind, "RANGE") && strings.EqualFold(start, "CURRENT ROW") {
			return "RANGE CURRENT ROW", true
		}
	case "TableAlias":
		// SQLite has no column list on a table alias -- a CTE's is fine --
		// and the reference drops it, which renames nothing the query can
		// then reach.
		cols, _ := e.Args["columns"].([]*Expression)
		if len(cols) > 0 && (e.Parent == nil || e.Parent.Class != "CTE") {
			return g.fail("a table alias naming its columns"), true
		}
	}
	return "", false
}

// sqliteTransformCreate is the reference's `_transform_create`: a
// single-column table PRIMARY KEY moves onto its column, and an
// AUTOINCREMENT is kept only where it follows a column's PRIMARY KEY --
// moved to just after it, or dropped where the column has none.
func sqliteTransformCreate(e *Expression) (*Expression, error) {
	if schema := e.This(); schema == nil || schema.Class != "Schema" {
		return e, nil
	}
	e = e.Copy()
	schema := e.This()
	items, _ := schema.Args["expressions"].([]*Expression)
	defs, order := map[string]*Expression{}, []string{}
	var pk *Expression
	for _, item := range items {
		switch item.Class {
		case "ColumnDef":
			if _, seen := defs[item.Name()]; !seen {
				order = append(order, item.Name())
			}
			defs[item.Name()] = item
		case "PrimaryKey":
			pk = item
		}
	}
	var keys []*Expression
	if pk != nil {
		keys, _ = pk.Args["expressions"].([]*Expression)
	}
	if len(keys) == 1 {
		column, ok := defs[keys[0].Name()]
		if !ok {
			return nil, fmt.Errorf("a PRIMARY KEY on %q, which names no column of the table", keys[0].Name())
		}
		column.Append("constraints", New("ColumnConstraint", Arg{"kind", New("PrimaryKeyColumnConstraint")}))
		schema.Set("expressions", without(items, pk))
	}
	for _, name := range order {
		placeAutoIncrement(defs[name])
	}
	return e, nil
}

// placeAutoIncrement moves a column's AUTOINCREMENT to just after its
// PRIMARY KEY, or drops it where the column has no PRIMARY KEY.
func placeAutoIncrement(column *Expression) {
	constraints, _ := column.Args["constraints"].([]*Expression)
	pk, auto := -1, -1
	for i, c := range constraints {
		kind, _ := c.Args["kind"].(*Expression)
		if kind == nil {
			continue
		}
		switch kind.Class {
		case "PrimaryKeyColumnConstraint":
			pk = i
		case "AutoIncrementColumnConstraint":
			auto = i
		}
	}
	if auto == -1 || (pk != -1 && auto > pk) {
		return
	}
	moved := constraints[auto]
	rest := without(constraints, moved)
	if pk != -1 {
		rest = append(rest[:pk:pk], append([]*Expression{moved}, rest[pk:]...)...)
	}
	column.Set("constraints", rest)
}

// without is the list less its first occurrence of one node.
func without(list []*Expression, drop *Expression) []*Expression {
	out := make([]*Expression, 0, len(list))
	for i, item := range list {
		if item == drop {
			return append(append(out, list[:i]...), list[i+1:]...)
		}
	}
	return append(out, list...)
}

// sqliteComputedColumn writes `AS (expr) [STORED] [NOT NULL]`, the
// expression parenthesised once.
func (g *generator) sqliteComputedColumn(e *Expression) string {
	if e.Args["data_type"] != nil {
		return g.fail("a generated column with a data type, which SQLite has no place for")
	}
	this, _ := e.Args["this"].(*Expression)
	out := g.node(this)
	if this == nil || this.Class != "Paren" {
		out = "(" + out + ")"
	}
	out = "AS " + out
	if e.Args["persisted"] == true {
		out += " STORED"
	}
	if e.Args["not_null"] == true {
		out += " NOT NULL"
	}
	return out
}

// sqliteGroupConcat writes GROUP_CONCAT with its DISTINCT in front of the
// first argument -- the reference keeps only the first of a DISTINCT's
// expressions -- and its ORDER BY inside the call after the arguments.
// The port DIVERGES here: the reference refuses the ORDER BY, but SQLite has
// ordered an aggregate's input since 3.44.
func (g *generator) sqliteGroupConcat(e *Expression) string {
	this, _ := e.Args["this"].(*Expression)
	ordering := ""
	if this != nil && this.Class == "Order" {
		ordering = " " + strings.TrimSpace(g.node(New("Order", Arg{"expressions", this.Args["expressions"]})))
		this, _ = this.Args["this"].(*Expression)
	}
	prefix := ""
	if this != nil && this.Class == "Distinct" {
		items, _ := this.Args["expressions"].([]*Expression)
		if len(items) == 0 {
			return g.fail("GROUP_CONCAT over an empty DISTINCT")
		}
		this, prefix = items[0], "DISTINCT "
	}
	args := []string{g.node(this)}
	if sep := g.child(e, "separator"); sep != "" {
		args = append(args, sep)
	}
	return "GROUP_CONCAT(" + prefix + strings.Join(args, ", ") + ordering + ")"
}

// sqliteTransaction writes `BEGIN [DEFERRED|IMMEDIATE|EXCLUSIVE]
// TRANSACTION`.
func (g *generator) sqliteTransaction(e *Expression) (string, bool) {
	if e.Args["modes"] != nil {
		return g.fail("transaction modes, which SQLite's BEGIN does not write"), true
	}
	this := ""
	switch v := e.Args["this"].(type) {
	case string:
		this = " " + v
	case *Expression:
		this = " " + g.node(v)
	}
	return "BEGIN" + this + " TRANSACTION", true
}
