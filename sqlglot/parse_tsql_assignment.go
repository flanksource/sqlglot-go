package sqlglot

// T-SQL's compound assignment, `@x += 1`, which the reference reads nowhere.
// It is the reference's assignment equality with the operator kept as an
// `operator` argument, so every reader of an assignment still sees an EQ.
// It is read only where T-SQL allows it: SET, an UPDATE's SET list, and a
// SELECT's variable assignment -- never as an ordinary comparison.

// compoundAssignmentOperators are T-SQL's `op=` assignments.
var compoundAssignmentOperators = map[TokenType]string{
	TokPLUS: "+", TokDASH: "-", TokSTAR: "*", TokSLASH: "/", TokMOD: "%", TokAMP: "&", TokCARET: "^", TokPIPE: "|",
}

// compoundAssignmentOperator is the operator of an `op=` at the cursor, which
// T-SQL writes as one token: `+ =` is not an assignment.
func (p *parser) compoundAssignmentOperator() string {
	c, n := p.curr(), p.next()
	if p.dialect != "tsql" || c == nil || n == nil || n.Type != TokEQ || c.End+1 != n.Start {
		return ""
	}
	return compoundAssignmentOperators[c.Type]
}

// parseCompoundAssignmentValue reads the `op= value` after an already read
// target, or returns nil without moving when no `op=` follows it.
func (p *parser) parseCompoundAssignmentValue(target *Expression) (*Expression, error) {
	operator := p.compoundAssignmentOperator()
	if operator == "" {
		return nil, nil
	}
	p.advance()
	p.advance()
	value, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	return New("EQ", Arg{"this", target}, Arg{"expression", value}, Arg{"operator", operator}), nil
}

// parseCompoundAssignment reads `target op= value` at the cursor, the target
// read by readTarget. When no `op=` follows the target it rewinds and returns
// nil, leaving the item to the ordinary expression reader.
func (p *parser) parseCompoundAssignment(readTarget func() (*Expression, error)) (*Expression, error) {
	if p.dialect != "tsql" {
		return nil, nil
	}
	mark := p.index
	target, err := readTarget()
	if err == nil && p.compoundAssignmentOperator() != "" {
		return p.parseCompoundAssignmentValue(target)
	}
	p.index = mark
	return nil, nil
}

// parseAssignmentTarget reads what an UPDATE's SET item assigns to: a
// variable or a column.
func (p *parser) parseAssignmentTarget() (*Expression, error) {
	if p.at(TokPARAMETER) {
		return p.parsePrimary()
	}
	return p.parseColumn()
}

// parseUpdateCompoundAssignment reads an UPDATE SET item's compound forms:
// `column op= value`, `@v op= value`, and `@v = column op= value`, which
// assigns the column and then the variable. Anything else returns nil with
// the cursor where it was.
func (p *parser) parseUpdateCompoundAssignment() (*Expression, error) {
	if assignment, err := p.parseCompoundAssignment(p.parseAssignmentTarget); err != nil || assignment != nil {
		return assignment, err
	}
	if p.dialect != "tsql" || !p.at(TokPARAMETER) {
		return nil, nil
	}
	mark := p.index
	variable, err := p.parsePrimary()
	if err != nil || !p.match(TokEQ) {
		p.index = mark
		return nil, nil
	}
	assignment, err := p.parseCompoundAssignment(p.parseColumn)
	if err != nil || assignment == nil {
		p.index = mark
		return nil, err
	}
	return New("EQ", Arg{"this", variable}, Arg{"expression", assignment}), nil
}

// parseSelectCompoundAssignment reads a SELECT item's `@v op= value`.
func (p *parser) parseSelectCompoundAssignment() (*Expression, error) {
	if !p.at(TokPARAMETER) {
		return nil, nil
	}
	return p.parseCompoundAssignment(p.parsePrimary)
}
