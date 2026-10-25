package sqlglot

import "strings"

// Raiserror, ExecuteAs and Revert are port-local statement nodes: the
// reference has no expression classes for these statement forms.
func (p *parser) parseTSQLRaiserror() (*Expression, error) {
	p.advance()
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("RAISERROR without parenthesized arguments")
	}
	var args []*Expression
	for {
		value, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		args = append(args, value)
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) || len(args) < 3 {
		return nil, p.unsupported("RAISERROR without its message, severity and state")
	}
	var options []string
	if p.match(TokWITH) {
		for {
			if !p.atWordsAny("LOG", "NOWAIT", "SETERROR") {
				return nil, p.unsupported("RAISERROR WITH without LOG, NOWAIT or SETERROR")
			}
			options = append(options, strings.ToUpper(p.curr().Text))
			p.advance()
			if !p.match(TokCOMMA) {
				break
			}
		}
	}
	return New("Raiserror", Arg{"expressions", args}, Arg{"options", options}), nil
}

func (p *parser) parseTSQLExecuteAs() (*Expression, error) {
	p.advance() // AS
	if p.atWordsAny("CALLER", "SELF", "OWNER") {
		context := strings.ToUpper(p.curr().Text)
		p.advance()
		return New("ExecuteAs", Arg{"this", context}), nil
	}
	if !p.atWordsAny("USER", "LOGIN") {
		return nil, p.unsupported("EXECUTE AS without CALLER, SELF, OWNER, USER or LOGIN")
	}
	context := strings.ToUpper(p.curr().Text)
	p.advance()
	if !p.match(TokEQ) || !p.atAny(TokSTRING, TokNATIONAL_STRING) {
		return nil, p.unsupported("EXECUTE AS USER or LOGIN without a string name")
	}
	name, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	return New("ExecuteAs", Arg{"this", context}, Arg{"expression", name}), nil
}

func (p *parser) parseTSQLDynamicExecute() (*Expression, error) {
	p.advance() // (
	value, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("EXECUTE without a closing parenthesis")
	}
	return New("Execute", Arg{"this", New("Paren", Arg{"this", value})}), nil
}
