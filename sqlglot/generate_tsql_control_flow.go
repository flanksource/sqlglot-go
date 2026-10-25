package sqlglot

func (g *generator) writeTSQLControlFlow(e *Expression) string {
	if g.dialect != "tsql" {
		return g.fail(e.Class + ", which this dialect does not write")
	}
	switch e.Class {
	case "Break":
		return "BREAK"
	case "Continue":
		return "CONTINUE"
	case "Goto", "Label":
		if e.This() == nil || e.This().Class != "Identifier" || e.Name() == "" {
			return g.fail(e.Class + " without a label name")
		}
		if e.Class == "Label" {
			return g.child(e, "this") + ":"
		}
		return "GOTO " + g.child(e, "this")
	default:
		return g.fail("an unknown control flow statement: " + e.Class)
	}
}
