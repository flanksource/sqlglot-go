package sqlglot

import "strings"

func (g *generator) writeTSQLDeclareCursor(e *Expression) string {
	if g.dialect != "tsql" {
		return g.fail("DECLARE CURSOR, which this dialect does not write")
	}
	if e.Args["this"] == nil || e.Args["expression"] == nil {
		return g.fail("DECLARE CURSOR without a name or query")
	}
	preOptions, _ := e.Args["pre_options"].([]string)
	options, _ := e.Args["options"].([]string)
	out := "DECLARE " + g.child(e, "this")
	if len(preOptions) > 0 {
		out += " " + strings.Join(preOptions, " ")
	}
	out += " CURSOR"
	if len(options) > 0 {
		out += " " + strings.Join(options, " ")
	}
	return out + " FOR " + g.child(e, "expression")
}

func (g *generator) writeTSQLCursorAction(e *Expression) string {
	if g.dialect != "tsql" {
		return g.fail("a cursor action, which this dialect does not write")
	}
	verb := map[string]string{"OpenCursor": "OPEN", "CloseCursor": "CLOSE", "DeallocateCursor": "DEALLOCATE"}[e.Class]
	if verb == "" || e.Args["this"] == nil {
		return g.fail("a cursor action without a name")
	}
	if global, _ := e.Args["global"].(bool); global {
		verb += " GLOBAL"
	}
	return verb + " " + g.child(e, "this")
}

func (g *generator) writeTSQLFetchCursor(e *Expression) string {
	if g.dialect != "tsql" {
		return g.fail("FETCH cursor, which this dialect does not write")
	}
	if e.Args["this"] == nil {
		return g.fail("FETCH without a cursor name")
	}
	out := "FETCH"
	if direction, _ := e.Args["direction"].(string); direction != "" {
		out += " " + direction
		if offset := g.child(e, "offset"); offset != "" {
			out += " " + offset
		}
		out += " FROM"
	}
	if global, _ := e.Args["global"].(bool); global {
		out += " GLOBAL"
	}
	out += " " + g.child(e, "this")
	if vars, _ := e.Args["into"].([]*Expression); len(vars) > 0 {
		parts := make([]string, len(vars))
		for i, variable := range vars {
			parts[i] = g.node(variable)
		}
		out += " INTO " + strings.Join(parts, ", ")
	}
	return out
}
