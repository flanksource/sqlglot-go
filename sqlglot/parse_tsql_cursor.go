package sqlglot

import "strings"

func (p *parser) startsTSQLCursorDeclaration() bool {
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
	var options []string
	for p.atWordsAny("LOCAL", "GLOBAL", "FORWARD_ONLY", "SCROLL", "STATIC", "KEYSET", "DYNAMIC", "FAST_FORWARD", "READ_ONLY", "SCROLL_LOCKS", "OPTIMISTIC", "TYPE_WARNING") {
		options = append(options, strings.ToUpper(p.curr().Text))
		p.advance()
	}
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
