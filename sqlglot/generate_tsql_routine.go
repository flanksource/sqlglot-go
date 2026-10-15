package sqlglot

// Writers for the T-SQL routine-body statements parse_tsql_routine.go reads.

// writeTSQLIfBlock writes `IF <cond> <branch> [ELSE <branch>]`. A Block branch
// is the reference's spelling, `BEGIN <statements>` with the END its own last
// statement; a single-statement branch -- the port's own reading -- is the
// statement alone.
func (g *generator) writeTSQLIfBlock(e *Expression) string {
	out := "IF " + g.child(e, "this") + " " + g.writeTSQLBranch(e.Args["true"])
	if otherwise, ok := e.Args["false"].(*Expression); ok && otherwise != nil {
		out += " ELSE " + g.writeTSQLBranch(otherwise)
	}
	return out
}

func (g *generator) writeTSQLBranch(branch any) string {
	node, _ := branch.(*Expression)
	if node == nil {
		return g.fail("an IF with nothing to run")
	}
	if node.Class == "Block" {
		return "BEGIN " + g.node(node)
	}
	return g.node(node)
}

// writeThrow writes `THROW <number>, <message>, <state>`, or a bare THROW.
// Only T-SQL has the statement.
func (g *generator) writeThrow(e *Expression) string {
	if g.dialect != "tsql" {
		return g.fail("THROW, which this dialect does not write")
	}
	if e.Args["this"] == nil {
		return "THROW"
	}
	return "THROW " + g.child(e, "this") + ", " + g.child(e, "message") + ", " + g.child(e, "state")
}
