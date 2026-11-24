package sqlglot

import "strings"

func (p *parser) parseSQLiteMigrationTrigger() (*Expression, error) {
	if p.atWords("IF", "NOT", "EXISTS") {
		p.advance()
		p.advance()
		p.advance()
	}
	name, err := p.parseTableName()
	if err != nil {
		return nil, err
	}
	switch {
	case p.atWords("INSTEAD", "OF"):
		p.advance()
		p.advance()
	case p.atWords("BEFORE"), p.atWords("AFTER"):
		p.advance()
	}
	if !p.atWordsAny("INSERT", "UPDATE", "DELETE") {
		return nil, p.unsupported("a SQLite trigger without an event")
	}
	update := strings.EqualFold(p.curr().Text, "UPDATE")
	p.advance()
	if update && p.atWords("OF") {
		p.advance()
		for {
			if _, err := p.parseIdentifier(); err != nil {
				return nil, err
			}
			if !p.match(TokCOMMA) {
				break
			}
		}
	}
	if !p.match(TokON) {
		return nil, p.unsupported("a SQLite trigger without ON")
	}
	target, err := p.parseTableName()
	if err != nil {
		return nil, err
	}
	if p.atWords("FOR", "EACH", "ROW") {
		p.advance()
		p.advance()
		p.advance()
	}
	var condition *Expression
	if p.atWords("WHEN") {
		p.advance()
		condition, err = p.parseExpression()
		if err != nil {
			return nil, err
		}
	}
	body, err := p.parseSQLiteTriggerBody()
	if err != nil {
		return nil, err
	}
	return New("Create", Arg{"kind", "TRIGGER"}, Arg{"this", name}, Arg{"target", target}, Arg{"when", condition}, Arg{"expression", body}), nil
}

func (p *parser) parseSQLiteTriggerBody() (*Expression, error) {
	if !p.match(TokBEGIN) {
		return nil, p.unsupported("a SQLite trigger without BEGIN")
	}
	var body []*Expression
	for p.curr() != nil && !p.at(TokEND) {
		start := p.index
		statement, err := p.parseStatement()
		if err != nil {
			return nil, err
		}
		if statement.Class == "Command" || p.index == start {
			return nil, p.unsupported("a SQLite trigger statement kept only as text")
		}
		p.locate(statement, start)
		body = append(body, statement)
		if !p.match(TokSEMICOLON) {
			return nil, p.unsupported("a SQLite trigger statement without a semicolon")
		}
	}
	if !p.match(TokEND) || len(body) == 0 {
		return nil, p.unsupported("a SQLite trigger without a complete body")
	}
	return New("Block", Arg{"expressions", body}), nil
}
