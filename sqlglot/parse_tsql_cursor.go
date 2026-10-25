package sqlglot

import "strings"

func (p *parser) startsTSQLCursorDeclaration() bool {
	if p.next() != nil && p.next().Type == TokPARAMETER {
		return false
	}
	i := p.index + 2
	for i < len(p.tokens) && (p.tokenIsWordAt(i, "INSENSITIVE") || p.tokenIsWordAt(i, "SCROLL")) {
		i++
	}
	return p.tokenIsWordAt(i, "CURSOR")
}

func (p *parser) parseTSQLDeclareCursor() (*Expression, error) {
	p.advance() // DECLARE
	name, err := p.parseTSQLCursorName()
	if err != nil {
		return nil, err
	}
	var preOptions []string
	for p.atWords("INSENSITIVE") || p.atWords("SCROLL") {
		preOptions = append(preOptions, strings.ToUpper(p.curr().Text))
		p.advance()
	}
	if !p.matchUnquotedWord("CURSOR") {
		return nil, p.unsupported("DECLARE cursor without CURSOR")
	}
	options := p.parseTSQLCursorOptions()
	if !p.matchUnquotedWord("FOR") || !p.at(TokSELECT) {
		return nil, p.unsupported("DECLARE CURSOR without FOR SELECT")
	}
	was := p.inRoutineBody
	p.inRoutineBody = true
	query, err := p.parseQuery()
	p.inRoutineBody = was
	if err != nil {
		return nil, err
	}
	return New("DeclareCursor", Arg{"this", name}, Arg{"pre_options", preOptions}, Arg{"options", options}, Arg{"expression", query}), nil
}

func (p *parser) parseTSQLCursorOptions() []string {
	var options []string
	for p.atWordsAny("LOCAL", "GLOBAL", "FORWARD_ONLY", "SCROLL", "STATIC", "KEYSET", "DYNAMIC", "FAST_FORWARD", "READ_ONLY", "SCROLL_LOCKS", "OPTIMISTIC", "TYPE_WARNING") {
		options = append(options, strings.ToUpper(p.curr().Text))
		p.advance()
	}
	return options
}

func (p *parser) parseTSQLSetCursor() (*Expression, error) {
	p.advance() // SET
	name := p.parseParameter()
	p.advance() // =
	p.advance() // CURSOR
	options := p.parseTSQLCursorOptions()
	if !p.matchUnquotedWord("FOR") || !p.at(TokSELECT) {
		return nil, p.unsupported("SET CURSOR without FOR SELECT")
	}
	whole := p.tokens
	depth := 0
	for i := p.index; i+1 < len(whole); i++ {
		if whole[i].Type == TokL_PAREN {
			depth++
		} else if whole[i].Type == TokR_PAREN {
			depth--
		} else if depth == 0 && p.tokenIsWordAt(i, "FOR") && p.tokenIsWordAt(i+1, "UPDATE") {
			p.tokens = whole[:i]
			break
		}
	}
	query, err := p.parseQuery()
	p.tokens = whole
	if err != nil {
		return nil, err
	}
	node := New("SetCursor", Arg{"this", name}, Arg{"options", options}, Arg{"expression", query})
	if p.atWords("FOR", "UPDATE") {
		p.advance()
		p.advance()
		node.Set("update", true)
		if p.matchUnquotedWord("OF") {
			var columns []*Expression
			for {
				column, err := p.parseColumn()
				if err != nil {
					return nil, err
				}
				columns = append(columns, column)
				if !p.match(TokCOMMA) {
					break
				}
			}
			node.Set("columns", columns)
		}
	}
	return node, nil
}

func (p *parser) parseTSQLCursorName() (*Expression, error) {
	if p.at(TokPARAMETER) {
		return p.parseParameter(), nil
	}
	return p.parseIdentifier()
}

func (p *parser) parseTSQLCursorAction() (*Expression, error) {
	verb := strings.ToUpper(p.curr().Text)
	p.advance()
	global := p.matchUnquotedWord("GLOBAL")
	name, err := p.parseTSQLCursorName()
	if err != nil {
		return nil, err
	}
	classes := map[string]string{"OPEN": "OpenCursor", "CLOSE": "CloseCursor", "DEALLOCATE": "DeallocateCursor"}
	return New(classes[verb], Arg{"this", name}, Arg{"global", global}), nil
}

func (p *parser) parseTSQLFetchCursor() (*Expression, error) {
	p.advance() // FETCH
	var direction string
	if p.atWordsAny("NEXT", "PRIOR", "FIRST", "LAST", "ABSOLUTE", "RELATIVE") {
		direction = strings.ToUpper(p.curr().Text)
		p.advance()
	}
	var offset *Expression
	if direction == "ABSOLUTE" || direction == "RELATIVE" {
		var err error
		offset, err = p.parseExpression()
		if err != nil {
			return nil, err
		}
	}
	if direction != "" && !p.match(TokFROM) {
		return nil, p.unsupported("FETCH direction without FROM")
	}
	global := p.matchUnquotedWord("GLOBAL")
	name, err := p.parseTSQLCursorName()
	if err != nil {
		return nil, err
	}
	var into []*Expression
	if p.match(TokINTO) {
		for {
			if !p.at(TokPARAMETER) {
				return nil, p.unsupported("FETCH INTO without a variable")
			}
			into = append(into, p.parseParameter())
			if !p.match(TokCOMMA) {
				break
			}
		}
	}
	return New("FetchCursor", Arg{"this", name}, Arg{"direction", direction}, Arg{"offset", offset}, Arg{"global", global}, Arg{"into", into}), nil
}
