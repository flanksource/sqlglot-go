package sqlglot

import "strings"

// parseConcatTier is SQLite's own `_parse_factor_operand`: `||`, `->` and
// `->>` form one left-associative tier that binds tighter than
// multiplication. A `||` chain under an arrow is parenthesised, and so is the
// whole tier where it stands next to arithmetic.
func (p *parser) parseConcatTier() (*Expression, error) {
	inArithmetic := p.index > 0 && p.isSQLiteArithmetic(p.tokens[p.index-1].Type)
	this, err := p.parseConcatOperand()
	if err != nil {
		return nil, err
	}
	parsed := false
	for c := p.curr(); c != nil && (c.Type == TokDPIPE || c.Type == TokARROW || c.Type == TokDARROW); c = p.curr() {
		if c.Type != TokDPIPE && this.Class == "DPipe" {
			this = New("Paren", Arg{"this", this})
		}
		if c.Type == TokDPIPE {
			p.advance()
			right, err := p.parseConcatOperand()
			if err != nil {
				return nil, err
			}
			this = New("DPipe", Arg{"this", this}, Arg{"expression", right},
				Arg{"safe", !p.tables.StrictStringConcat})
		} else if this, err = p.consumeJSONArrow(this, p.parseConcatOperand); err != nil {
			return nil, err
		}
		parsed = true
	}
	if parsed && (inArithmetic || (p.curr() != nil && p.isSQLiteArithmetic(p.curr().Type))) {
		this = New("Paren", Arg{"this", this})
	}
	return this, nil
}

// isSQLiteArithmetic reports a token of the tiers around SQLite's concat
// tier -- bitwise, term and factor, COLLATE excepted -- next to which that
// tier is parenthesised so it keeps its grouping in other dialects.
func (p *parser) isSQLiteArithmetic(t TokenType) bool {
	_, term := p.tables.Term[t]
	_, factor := p.tables.Factor[t]
	_, bitwise := p.tables.Bitwise[t]
	return t != TokCOLLATE && (term || factor || bitwise)
}

// parseConcatOperand is a unary with any COLLATE after it: COLLATE binds
// tighter than every binary operator, this tier included.
func (p *parser) parseConcatOperand() (*Expression, error) {
	this, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for p.match(TokCOLLATE) {
		name, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		this = New("Collate", Arg{"this", this}, Arg{"expression", collationName(name)})
	}
	return this, nil
}

// standaloneUnique reports a UNIQUE that is a whole constraint because
// another constraint's keyword follows it (`UNIQUE REFERENCES t`).
func (p *parser) standaloneUnique() bool {
	c := p.curr()
	if c == nil || p.tables.StandaloneUniqueBefore == nil {
		return false
	}
	_, ok := p.tables.StandaloneUniqueBefore[strings.ToUpper(c.Text)]
	return ok
}

// parseSQLitePragma is SQLite's own `_parse_pragma`: a name, perhaps
// qualified, and perhaps a value after `=` or in parentheses.
func (p *parser) parseSQLitePragma() (*Expression, error) {
	p.advance() // PRAGMA
	name := p.parseAnyVar()
	if name == nil {
		return nil, p.unsupported("PRAGMA without a name")
	}
	if p.match(TokDOT) {
		field := p.parseAnyVar()
		if field == nil {
			return nil, p.unsupported("PRAGMA qualified by nothing")
		}
		name = New("Dot", Arg{"this", name}, Arg{"expression", field})
	}
	p.match(TokEQ)
	value, err := p.parsePragmaValue()
	if err != nil {
		return nil, err
	}
	if value == nil {
		return New("Pragma", Arg{"this", name}), nil
	}
	return New("Pragma", Arg{"this", New("EQ", Arg{"this", name}, Arg{"expression", value})}), nil
}

// parsePragmaValue reads a unary, or any single word, optionally wrapped.
func (p *parser) parsePragmaValue() (*Expression, error) {
	if p.curr() == nil {
		return nil, nil
	}
	wrapped := p.match(TokL_PAREN)
	value, err := p.parseUnary()
	if err != nil {
		value = p.parseAnyVar()
		if value == nil {
			return nil, err
		}
	}
	if wrapped && !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed PRAGMA value")
	}
	return value, nil
}

// parseAnyVar reads any one token as a Var, the reference's
// `_parse_var(any_token=True)`.
func (p *parser) parseAnyVar() *Expression {
	c := p.curr()
	if c == nil || c.Type == TokSEMICOLON || c.Type == TokL_PAREN || c.Type == TokR_PAREN ||
		c.Type == TokDOT || c.Type == TokEQ {
		return nil
	}
	p.advance()
	return New("Var", Arg{"this", c.Text})
}

// sqliteJSONExtract is SQLite's own JSON_EXTRACT builder: one path returns
// what `->>` returns, except that an object or array keeps its JSON subtype,
// so it is a scalar extraction flagged json_subtype; several paths stay a
// JSONExtract.
func sqliteJSONExtract(node *Expression) *Expression {
	if node.Args["expressions"] != nil {
		return node
	}
	return New("JSONExtractScalar",
		Arg{"this", node.Args["this"]}, Arg{"expression", node.Args["expression"]},
		Arg{"scalar_only", false}, Arg{"json_subtype", true})
}

// parseSQLiteAttachDetach is SQLite's own: an optional DATABASE, then one
// expression, aliased or not.
func (p *parser) parseSQLiteAttachDetach() (*Expression, error) {
	class := "Detach"
	if p.at(TokATTACH) {
		class = "Attach"
	}
	p.advance()
	p.match(TokDATABASE)
	this, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	if this, err = p.parseAlias(this); err != nil {
		return nil, err
	}
	return New(class, Arg{"this", this}), nil
}
