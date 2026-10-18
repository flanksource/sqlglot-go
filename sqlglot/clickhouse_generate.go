package sqlglot

import "strings"

// clickhouseDateDeltas are the reference ClickHouse generator's
// `_datetime_delta_sql` names: with a unit, the unit comes FIRST and the
// amount before the date -- `DATE_ADD(DAY, 1, x)`.
var clickhouseDateDeltas = map[string]string{
	"DateAdd": "DATE_ADD", "DateDiff": "DATE_DIFF", "DateSub": "DATE_SUB",
	"TimestampAdd": "TIMESTAMP_ADD", "TimestampSub": "TIMESTAMP_SUB",
}

// clickhouseNode writes the nodes ClickHouse's own generator overrides, where
// the generated spellings cannot say it. ok is false where the generic
// writers apply.
func (g *generator) clickhouseNode(e *Expression) (string, bool) {
	if name, ok := clickhouseDateDeltas[e.Class]; ok && e.Args["unit"] != nil {
		return g.clickhouseDateDelta(e, name), true
	}
	switch e.Class {
	case "Placeholder":
		if e.Args["kind"] == nil {
			return "", false
		}
		return g.clickhouseQueryParameter(e), true
	case "GroupConcat":
		return g.clickhouseGroupConcat(e), true
	case "VarMap":
		// `map(...)`, lower-cased; under SETTINGS it is a brace literal,
		// which is not written here.
		if e.Parent != nil && e.Parent.Args["settings"] != nil {
			return g.fail("a map under SETTINGS"), true
		}
		out := g.writeVarMap(e)
		if open := strings.Index(out, "("); open > 0 {
			out = strings.ToLower(out[:open]) + out[open:]
		}
		return out, true
	case "JSONExtract", "JSONExtractScalar":
		// JSONExtractString(x, 'a', 1) is written segment by segment, its
		// subscripts counted from one; not ported.
		return g.fail(e.Class + " written as ClickHouse path segments"), true
	}
	return "", false
}

// clickhouseQueryParameter writes `{name: Type}`, the name bare whether or
// not it was quoted -- the reference's `.name`.
func (g *generator) clickhouseQueryParameter(e *Expression) string {
	name := ""
	if this, _ := e.Args["this"].(*Expression); this != nil {
		name = this.Name()
	}
	kind := ""
	switch k := e.Args["kind"].(type) {
	case string:
		kind = k
	case *Expression:
		kind = g.node(k)
	}
	return "{" + name + ": " + kind + "}"
}

func (g *generator) clickhouseDateDelta(e *Expression, name string) string {
	unit, _ := e.Args["unit"].(*Expression)
	switch unit.Class {
	case "Var", "Placeholder", "WeekStart", "Column":
	default:
		if unit.Name() == "" {
			return g.fail(e.Class + " whose unit is not a word")
		}
		unit = New("Var", Arg{"this", unit.Name()})
	}
	args := []string{g.node(unit), g.child(e, "expression"), g.child(e, "this")}
	if zone := g.child(e, "zone"); zone != "" {
		args = append(args, zone)
	}
	return name + "(" + strings.Join(args, ", ") + ")"
}

// clickhouseGroupConcat writes `groupConcat(x)`, or the parametric
// `groupConcat(sep[, limit])(x)` where a separator or a limit was given.
func (g *generator) clickhouseGroupConcat(e *Expression) string {
	this, _ := e.Args["this"].(*Expression)
	separator := g.child(e, "separator")
	if this != nil && this.Class == "Limit" && this.This() != nil {
		params := []string{g.child(this, "expression")}
		if separator != "" {
			params = append([]string{separator}, params...)
		}
		return "groupConcat(" + strings.Join(params, ", ") + ")(" + g.node(this.This()) + ")"
	}
	if separator != "" {
		return "groupConcat(" + separator + ")(" + g.node(this) + ")"
	}
	return "groupConcat(" + g.node(this) + ")"
}
