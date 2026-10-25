package sqlglot

func (p *parser) atTSQLTableKey() bool {
	if p.dialect != "tsql" {
		return false
	}
	i := p.index
	if i < len(p.tokens) && p.tokens[i].Type == TokCONSTRAINT {
		i += 2
	}
	if !p.tokenIsWordAt(i, "UNIQUE") && !p.tokenIsWordAt(i, "PRIMARY KEY") && !p.tokenIsWordAt(i, "FOREIGN KEY") {
		return false
	}
	i++
	return i < len(p.tokens) && p.tokens[i].Type == TokL_PAREN
}

func (p *parser) startsTSQLNodes() bool {
	if p.dialect != "tsql" {
		return false
	}
	start := p.index + 1
	if p.at(TokPARAMETER) {
		start++
	}
	for i := start; i+2 < len(p.tokens); i++ {
		if p.tokens[i].Type != TokDOT {
			return false
		}
		if p.tokenIsWordAt(i+1, "nodes") && p.tokens[i+2].Type == TokL_PAREN {
			return true
		}
		i++
	}
	return false
}

func (p *parser) parseTSQLNodes() (*Expression, error) {
	var receiver *Expression
	if p.at(TokPARAMETER) {
		receiver = p.parseParameter()
	} else {
		id, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		receiver = id
	}
	for p.match(TokDOT) {
		if p.atWords("nodes") && p.next() != nil && p.next().Type == TokL_PAREN {
			p.advance()
			args, err := p.parseParenthesisedList()
			if err != nil {
				return nil, err
			}
			if len(args) != 1 {
				return nil, p.unsupported("XML nodes() without exactly one XPath argument")
			}
			return New("Dot", Arg{"this", receiver}, Arg{"expression", New("Anonymous", Arg{"this", "nodes"}, Arg{"expressions", args})}), nil
		}
		id, err := p.parseIdentifier()
		if err != nil {
			return nil, err
		}
		receiver = New("Dot", Arg{"this", receiver}, Arg{"expression", id})
	}
	return nil, p.unsupported("an XML rowset without nodes()")
}
