package sqlglot

// ClickHouse's own FUNCTION_PARSERS: calls whose arguments are read by a
// grammar of their own. Entered with the name current and `(` after it.
func (p *parser) parseClickHouseSyntaxFunction(upper string) (*Expression, bool, error) {
	var fn *Expression
	var err error
	switch upper {
	case "ARRAYJOIN":
		fn, err = p.parseClickHouseCall(func() (*Expression, error) {
			this, err := p.parseExpression()
			return New("Explode", Arg{"this", this}), err
		})
	case "TUPLE":
		fn, err = p.parseClickHouseCall(func() (*Expression, error) {
			args, err := p.parseClickHouseArgs(true)
			return New("Struct", Arg{"expressions", args}), err
		})
	case "XOR", "AND", "OR":
		fn, err = p.parseClickHouseCall(func() (*Expression, error) { return p.parseConnectorCall(upper) })
	case "QUANTILE", "MEDIAN":
		fn, err = p.parseClickHouseCall(p.parseClickHouseQuantile)
	case "GROUPCONCAT":
		fn, err = p.parseClickHouseCall(p.parseClickHouseGroupConcat)
	default:
		return nil, false, nil
	}
	return fn, true, err
}

// parseClickHouseCall reads the parentheses around a body the caller reads.
func (p *parser) parseClickHouseCall(body func() (*Expression, error)) (*Expression, error) {
	p.advance()
	p.advance()
	fn, err := body()
	if err != nil {
		return nil, err
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed call")
	}
	return fn, nil
}

func (p *parser) parseClickHouseArgs(alias bool) ([]*Expression, error) {
	var args []*Expression
	for !p.at(TokR_PAREN) {
		arg, err := p.parseCallArgumentAliased(alias)
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
		if !p.match(TokCOMMA) {
			break
		}
	}
	return args, nil
}

// parseConnectorCall reads `xor(a, b, c)`, `and(a, b)` and `or(a, b)` as the
// chain of connectors the reference builds, a connector operand wrapped in
// parentheses; AND and OR are wrapped whole as well.
func (p *parser) parseConnectorCall(upper string) (*Expression, error) {
	args, err := p.parseClickHouseArgs(false)
	if err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return nil, p.unsupported(upper + " without arguments")
	}
	class := map[string]string{"XOR": "Xor", "AND": "And", "OR": "Or"}[upper]
	this := args[0]
	if len(args) > 1 {
		this = wrapConnector(this)
	}
	for _, next := range args[1:] {
		this = New(class, Arg{"this", this}, Arg{"expression", wrapConnector(next)})
	}
	if class == "Xor" {
		return this, nil
	}
	return New("Paren", Arg{"this", this}), nil
}

func wrapConnector(e *Expression) *Expression {
	switch e.Class {
	case "And", "Or", "Xor":
		return New("Paren", Arg{"this", e})
	}
	return e
}

// parseClickHouseQuantile reads `quantile(0.5)(x)` -- the level, then the
// value in a second list -- and a bare `median(x)`, the 0.5 level implied.
func (p *parser) parseClickHouseQuantile() (*Expression, error) {
	this, err := p.parseCallArgument()
	if err != nil {
		return nil, err
	}
	params, err := p.parseInnerAggregateParams()
	if err != nil {
		return nil, err
	}
	if len(params) > 0 {
		return New("Quantile", Arg{"this", params[0]}, Arg{"quantile", this}), nil
	}
	half := New("Literal", Arg{"this", "0.5"}, Arg{"is_string", false})
	return New("Quantile", Arg{"this", this}, Arg{"quantile", half}), nil
}

// parseClickHouseGroupConcat reads `groupConcat(x)` and the parametric
// `groupConcat(sep[, limit])(x)`.
func (p *parser) parseClickHouseGroupConcat() (*Expression, error) {
	args, err := p.parseClickHouseArgs(false)
	if err != nil {
		return nil, err
	}
	params, err := p.parseInnerAggregateParams()
	if err != nil {
		return nil, err
	}
	if len(params) == 0 {
		return New("GroupConcat", Arg{"this", argAt(args, 0)}), nil
	}
	this := params[0]
	if limit := argAt(args, 1); limit != nil {
		this = New("Limit", Arg{"this", this}, Arg{"expression", limit})
	}
	return New("GroupConcat", Arg{"this", this}, Arg{"separator", argAt(args, 0)}), nil
}

// braceMap is ClickHouse's `{'a': 1}`: a map of string keys, not a struct.
func braceMap(items []*Expression) *Expression {
	keys := make([]*Expression, 0, len(items))
	values := make([]*Expression, 0, len(items))
	for _, item := range items {
		keys = append(keys, New("Literal", Arg{"this", item.This().Name()}, Arg{"is_string", true}))
		values = append(values, childOf(item, "expression"))
	}
	return New("VarMap",
		Arg{"keys", New("Array", Arg{"expressions", keys})},
		Arg{"values", New("Array", Arg{"expressions", values})})
}

// parseQueryParameter reads a ClickHouse query parameter, `{name: Type}` or
// `{name: Identifier}`, or nothing -- the tokens untouched -- where the
// braces hold something else.
func (p *parser) parseQueryParameter() (*Expression, error) {
	start := p.index
	p.advance()
	name := p.curr()
	if name == nil || (name.Type != TokVAR && name.Type != TokIDENTIFIER && !p.atIdentifier()) {
		p.index = start
		return nil, nil
	}
	p.advance()
	p.match(TokCOLON)
	var kind any
	if p.atWords("IDENTIFIER") {
		p.advance()
		kind = "Identifier"
	} else if c := p.curr(); c != nil && p.isTypeToken(c) {
		dt, err := p.parseDataType()
		if err != nil {
			return nil, err
		}
		kind = dt
	}
	if kind == nil {
		p.index = start
		return nil, nil
	}
	if !p.match(TokR_BRACE) {
		return nil, p.unsupported("query parameter without }")
	}
	var this any = New("Var", Arg{"this", name.Text})
	if name.Type == TokIDENTIFIER {
		this = New("Identifier", Arg{"this", name.Text}, Arg{"quoted", true})
	}
	return New("Placeholder", Arg{"this", this}, Arg{"kind", kind}), nil
}

func (p *parser) isTypeToken(c *Token) bool {
	_, isType := p.tables.TypeTokens[c.Type]
	return isType || c.Type == TokNULLABLE
}

// parseInnerAggregateParams reads the second argument list of a parametric
// aggregate whose first one is still open: `)(` closes one and opens the
// next, whose own `)` the caller reads.
func (p *parser) parseInnerAggregateParams() ([]*Expression, error) {
	if !p.atPair(TokR_PAREN, TokL_PAREN) {
		return nil, nil
	}
	p.advance()
	p.advance()
	return p.parseClickHouseArgs(false)
}
