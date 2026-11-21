package access

import (
	"strings"

	"github.com/flanksource/sqlglot-go/sqlglot"
)

// execute records EXEC / EXECUTE as a call to the stored procedure, and
// `EXEC (<sql>)` or `EXEC sp_executesql <sql>` as the statements <sql> runs.
func (r *reader) execute(e *sqlglot.Expression) error {
	if this := e.This(); this != nil && this.Class == "Paren" {
		return r.dynamicSQL(this.This())
	}
	ref, ok := tableRefOf(e.This())
	if !ok {
		return &UnsupportedSQLError{Class: e.Class}
	}
	if strings.EqualFold(ref.name, "sp_executesql") && (ref.schema == "" || strings.EqualFold(ref.schema, "sys")) {
		args := children(e)
		if len(args) == 0 {
			return &UnsupportedSQLError{Class: "dynamic SQL"}
		}
		return r.dynamicSQL(args[0])
	}
	r.call(ref)
	return nil
}

// dynamicSQL reads the statements sql runs when it is a string literal; any
// other value is only known when it runs.
func (r *reader) dynamicSQL(sql *sqlglot.Expression) error {
	literal := sql != nil && (sql.Class == "National" || sql.Class == "Literal" && sql.Args["is_string"] == true)
	if !literal || r.ctx.dynamic == nil {
		return &UnsupportedSQLError{Class: "dynamic SQL"}
	}
	nodes, err := r.ctx.dynamic(sql.Name())
	if err != nil {
		return err
	}
	r.dynamic = append(r.dynamic, nodes...)
	return nil
}

func (r *reader) insert(e *sqlglot.Expression) error {
	if err := r.with(e); err != nil {
		return err
	}
	target := e.This()
	if target.Class == "Schema" {
		target = target.This()
	}
	r.targets[target] = true
	if err := r.source(target); err != nil {
		return err
	}
	return r.walkArgs(e, "this", "with_")
}

func (r *reader) update(e *sqlglot.Expression) error {
	if err := r.with(e); err != nil {
		return err
	}
	from := child(e, "from_")
	if err := r.dmlTarget(e.This(), from.This()); err != nil {
		return err
	}
	if from != nil {
		if err := r.source(from.This()); err != nil {
			return err
		}
	}
	return r.walkArgs(e, "this", "with_")
}

func (r *reader) delete(e *sqlglot.Expression) error {
	if err := r.with(e); err != nil {
		return err
	}
	from := e.This()
	if targets := list(e, "tables"); len(targets) > 0 {
		for _, target := range targets {
			if err := r.dmlTarget(target, from); err != nil {
				return err
			}
		}
	} else {
		r.targets[from] = true
	}
	if err := r.source(from); err != nil {
		return err
	}
	for _, using := range list(e, "using") {
		if err := r.source(using); err != nil {
			return err
		}
	}
	return r.walkArgs(e, "this", "tables", "using", "with_")
}

// dmlTarget marks the table an UPDATE or DELETE writes. T-SQL's `UPDATE a …
// FROM Orders a` names a FROM item by its alias, so the FROM item is the
// target; otherwise the named table is, and it is sourced here.
func (r *reader) dmlTarget(target, from *sqlglot.Expression) error {
	if ref, ok := tableRefOf(target); ok && ref.schema == "" && child(target, "alias") == nil {
		for _, item := range fromItems(from) {
			if item.Class == "Table" && strings.EqualFold(aliasOf(item), ref.name) {
				r.targets[item] = true
				return nil
			}
		}
	}
	r.targets[target] = true
	return r.source(target)
}

func fromItems(from *sqlglot.Expression) []*sqlglot.Expression {
	if from == nil {
		return nil
	}
	items := []*sqlglot.Expression{from}
	for _, join := range list(from, "joins") {
		items = append(items, join.This())
	}
	return items
}

func (r *reader) merge(e *sqlglot.Expression) error {
	if err := r.with(e); err != nil {
		return err
	}
	target := e.This()
	r.targets[target] = true
	for _, item := range []*sqlglot.Expression{target, child(e, "using")} {
		if err := r.source(item); err != nil {
			return err
		}
	}
	if err := r.walk(child(e, "on")); err != nil {
		return err
	}
	for _, when := range children(child(e, "whens")) {
		if err := r.mergeWhen(when); err != nil {
			return err
		}
	}
	return r.walk(child(e, "returning"))
}

// mergeWhen walks a WHEN branch's condition and the values it assigns or
// inserts; the columns it names belong to the target.
func (r *reader) mergeWhen(when *sqlglot.Expression) error {
	if err := r.walk(child(when, "condition")); err != nil {
		return err
	}
	then := child(when, "then")
	switch then.Class {
	case "Update":
		for _, set := range children(then) {
			if err := r.walk(child(set, "expression")); err != nil {
				return err
			}
		}
	case "Insert":
		return r.walk(child(then, "expression"))
	}
	return nil
}
