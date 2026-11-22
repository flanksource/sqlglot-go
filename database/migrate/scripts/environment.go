package scripts

import (
	"slices"

	"github.com/flanksource/sqlglot-go/sqlglot"
)

// environmentBuiltins are the built-ins tracked by value: each answers the same
// to every connection until the target itself changes.
var environmentBuiltins = []string{"DB_NAME"}

// constantFunctions are the parsed pure functions an environment expression may
// wrap a built-in in, besides pureStringBuiltin's.
var constantFunctions = []string{"Substring", "Upper", "Lower", "Left", "Right", "Trim", "Length", "Reverse", "Replace"}

// environmentExpression is the largest call around e, an environment built-in,
// whose value depends on nothing else: SUBSTRING((SELECT DB_NAME()),5,3) is
// "SUBSTRING(DB_NAME(), 5, 3)". It is empty when e's own arguments are not
// constant.
func environmentExpression(e *sqlglot.Expression) (string, error) {
	if !constantExpression(e) {
		return "", nil
	}
	for e.Parent != nil && constantExpression(e.Parent) {
		e = e.Parent
	}
	return sqlglot.Generate(unwrapped(e.Copy()), "tsql")
}

func constantExpression(e *sqlglot.Expression) bool {
	children := constantChildren(e)
	switch {
	case e.Class == "Literal", e.Class == "National":
		return true
	case e.Class == "Paren":
	case e.Class == "Cast":
		return constantExpression(e.This())
	case e.Class == "Subquery":
		if len(children) != 1 || children[0].Class != "Select" {
			return false
		}
	case e.Class == "Select":
		if expressions, _ := e.Args["expressions"].([]*sqlglot.Expression); len(expressions) != 1 || len(children) != 1 {
			return false
		}
	case e.Class == "Anonymous":
		builtin, ok := e.Builtin("tsql")
		if !ok && !pureStringBuiltin(e.Name()) || ok && !slices.Contains(environmentBuiltins, builtin.Name) {
			return false
		}
	case !slices.Contains(constantFunctions, e.Class):
		return false
	}
	return !slices.ContainsFunc(children, func(child *sqlglot.Expression) bool { return !constantExpression(child) })
}

func constantChildren(e *sqlglot.Expression) []*sqlglot.Expression {
	var out []*sqlglot.Expression
	for _, key := range e.Keys {
		switch value := e.Args[key].(type) {
		case *sqlglot.Expression:
			if value != nil {
				out = append(out, value)
			}
		case []*sqlglot.Expression:
			out = append(out, value...)
		}
	}
	return out
}

// unwrapped is e without the parentheses and scalar selects around a constant.
func unwrapped(e *sqlglot.Expression) *sqlglot.Expression {
	switch e.Class {
	case "Paren", "Subquery", "Select":
		return unwrapped(constantChildren(e)[0])
	case "Left", "Right":
		// The parser casts their argument to text, which T-SQL itself refuses.
		if cast := e.This(); cast.Class == "Cast" {
			e.Set("this", cast.This())
		}
	}
	for _, key := range e.Keys {
		switch value := e.Args[key].(type) {
		case *sqlglot.Expression:
			if value != nil {
				e.Set(key, unwrapped(value))
			}
		case []*sqlglot.Expression:
			for i := range value {
				value[i] = unwrapped(value[i])
			}
			e.Set(key, value)
		}
	}
	return e
}
