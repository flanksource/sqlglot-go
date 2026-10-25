package sqlglot

import "strings"

func (g *generator) writeTSQLRaiserror(e *Expression) string {
	if g.dialect != "tsql" {
		return g.fail("RAISERROR, which this dialect does not write")
	}
	args, _ := e.Args["expressions"].([]*Expression)
	if len(args) < 3 {
		return g.fail("RAISERROR without its message, severity and state")
	}
	values := make([]string, len(args))
	for i, value := range args {
		values[i] = g.node(value)
	}
	out := "RAISERROR (" + strings.Join(values, ", ") + ")"
	if options, _ := e.Args["options"].([]string); len(options) > 0 {
		for _, option := range options {
			if option != "LOG" && option != "NOWAIT" && option != "SETERROR" {
				return g.fail("RAISERROR with an unknown option")
			}
		}
		out += " WITH " + strings.Join(options, ", ")
	}
	return out
}

func (g *generator) writeTSQLExecuteAs(e *Expression) string {
	if g.dialect != "tsql" {
		return g.fail("EXECUTE AS, which this dialect does not write")
	}
	context, _ := e.Args["this"].(string)
	switch context {
	case "CALLER", "SELF", "OWNER":
		return "EXECUTE AS " + context
	case "USER", "LOGIN":
		if e.Args["expression"] == nil {
			return g.fail("EXECUTE AS without a name")
		}
		return "EXECUTE AS " + context + " = " + g.child(e, "expression")
	default:
		return g.fail("EXECUTE AS without a valid context")
	}
}

func (g *generator) writeTSQLRevert(e *Expression) string {
	if g.dialect != "tsql" {
		return g.fail("REVERT, which this dialect does not write")
	}
	return "REVERT"
}
