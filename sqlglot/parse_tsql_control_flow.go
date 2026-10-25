package sqlglot

func (p *parser) atTSQLLabel() bool {
	return p.atIdentifier() && p.next() != nil && p.next().Type == TokCOLON
}

// These statements have no upstream expression classes. Goto and Label keep
// their target/name as an Identifier in this; Break and Continue have no args.
func (p *parser) parseTSQLControlFlow() (*Expression, error) {
	if p.atTSQLLabel() {
		name, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		p.advance() // :
		return New("Label", Arg{"this", name}), nil
	}
	if p.atWords("GOTO") {
		p.advance()
		if p.atStatementEnd() || !p.atIdentifier() {
			return nil, p.unsupported("GOTO without a label name")
		}
		name, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		return New("Goto", Arg{"this", name}), nil
	}
	class := "Break"
	if p.atWords("CONTINUE") {
		class = "Continue"
	}
	p.advance()
	return New(class), nil
}
