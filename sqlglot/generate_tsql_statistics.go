package sqlglot

import "strings"

func (g *generator) writeTSQLUpdateStatistics(e *Expression) string {
	if g.dialect != "tsql" || e.Args["this"] == nil {
		return g.fail("UPDATE STATISTICS without a table, or outside T-SQL")
	}
	out := "UPDATE STATISTICS " + g.child(e, "this")
	if indexes, _ := e.Args["expressions"].([]*Expression); len(indexes) > 0 {
		parts := make([]string, len(indexes))
		for i, index := range indexes {
			parts[i] = g.node(index)
		}
		if len(parts) == 1 {
			out += " " + parts[0]
		} else {
			out += " (" + strings.Join(parts, ", ") + ")"
		}
	}
	if options, _ := e.Args["options"].([]*Expression); len(options) > 0 {
		parts := make([]string, len(options))
		for i, option := range options {
			parts[i] = g.node(option)
		}
		out += " WITH " + strings.Join(parts, ", ")
	}
	return out
}

func (g *generator) writeTSQLStatisticsSample(e *Expression) string {
	unit, _ := e.Args["unit"].(string)
	if g.dialect != "tsql" || e.Args["this"] == nil || (unit != "PERCENT" && unit != "ROWS") {
		return g.fail("a statistics SAMPLE without a value or unit, or outside T-SQL")
	}
	return "SAMPLE " + g.child(e, "this") + " " + unit
}
