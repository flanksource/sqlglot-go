package sqlglot

import "strings"

// writeNullableDataType writes a type and, where the dialect spells
// nullability as part of the type, wraps it the way the reference's
// ClickHouse generator does: a type marked nullable, or one whose
// nullability the tree leaves open, becomes `Nullable(T)` -- unless it is a
// MAP's key, or a kind that can never be nullable. A type ClickHouse itself
// read is marked non-nullable and is written bare.
func (g *generator) writeNullableDataType(e *Expression) string {
	out := g.writeDataType(e)
	if g.tables.NullableTemplate == "" || !g.wrapsNullable(e) {
		return out
	}
	return strings.ReplaceAll(g.tables.NullableTemplate, "{type}", out)
}

func (g *generator) wrapsNullable(e *Expression) bool {
	switch nullable := e.Args["nullable"].(type) {
	case bool:
		return nullable
	case nil:
	default:
		return false
	}
	if isMapKeyType(e) {
		return false
	}
	kind, _ := e.Args["this"].(DataTypeKind)
	_, never := g.tables.NeverNullableTypes[string(kind)]
	return !never
}

// parseNullableType reads `Nullable(T)` as T flagged nullable.
func (p *parser) parseNullableType() (*Expression, error) {
	p.advance()
	p.advance()
	member, err := p.parseCollatedDataType()
	if err != nil {
		return nil, err
	}
	if member == nil || member.Class != "DataType" || !p.match(TokR_PAREN) {
		return nil, p.unsupported("Nullable over anything but one type")
	}
	member.Set("nullable", true)
	return member, nil
}

// markNonNullable is ClickHouse's own `_parse_types`: every type it reads
// is non-nullable unless it said Nullable, which is what lets a type read
// from another dialect be told apart on the way out.
func (p *parser) markNonNullable(dt *Expression) {
	if p.tables.NullableTemplate == "" || dt == nil || dt.Class != "DataType" {
		return
	}
	if nullable, _ := dt.Args["nullable"].(bool); !nullable {
		dt.Set("nullable", false)
	}
}

// parseAggregateCombinator turns an Anonymous call whose name is a ClickHouse
// aggregate -- bare, or under any stack of combinator suffixes -- into the
// aggregate node the reference builds, reading the second argument list a
// parametric aggregate takes: `quantile(0.5)(x)`.
func (p *parser) parseAggregateCombinator(anon *Expression) (*Expression, error) {
	if len(p.tables.AggFunctions) == 0 {
		return anon, nil
	}
	name := anon.Name()
	if id, _ := anon.Args["this"].(*Expression); id != nil {
		name = id.Name()
	}
	isAgg, combined := p.resolveAggregate(name)
	if !isAgg {
		return anon, nil
	}
	params, err := p.parseAggregateParams()
	if err != nil {
		return nil, err
	}
	class := map[[2]bool]string{
		{false, false}: "AnonymousAggFunc", {false, true}: "ParameterizedAgg",
		{true, false}: "CombinedAggFunc", {true, true}: "CombinedParameterizedAgg",
	}[[2]bool{combined, params != nil}]
	agg := New(class, Arg{"this", anon.Args["this"]}, Arg{"expressions", anon.Args["expressions"]})
	if params != nil {
		agg.Set("params", params)
	}
	return agg, nil
}

// resolveAggregate is the reference's `_resolve_clickhouse_agg`: strip
// suffixes, longest first, until what is left is an aggregate with at most
// one suffix of its own. It reports whether the name is an aggregate at all,
// and whether any combinator was applied.
func (p *parser) resolveAggregate(name string) (isAgg, combined bool) {
	for {
		if _, ok := p.tables.AggFunctions[name]; ok {
			return true, combined
		}
		if p.aggregateWithOneSuffix(name) {
			return true, true
		}
		stripped := false
		for _, s := range p.tables.AggFunctionSuffixes {
			if strings.HasSuffix(name, s) && len(name) != len(s) {
				name, combined, stripped = name[:len(name)-len(s)], true, true
				break
			}
		}
		if !stripped {
			return false, false
		}
	}
}

func (p *parser) aggregateWithOneSuffix(name string) bool {
	for _, s := range p.tables.AggFunctionSuffixes {
		if _, ok := p.tables.AggFunctions[strings.TrimSuffix(name, s)]; ok && strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

// parseAggregateParams reads the argument list after a parametric
// aggregate's parameters, or nothing where none follows.
func (p *parser) parseAggregateParams() ([]*Expression, error) {
	if !p.match(TokL_PAREN) {
		return nil, nil
	}
	var params []*Expression
	for !p.at(TokR_PAREN) {
		arg, err := p.parseCallArgument()
		if err != nil {
			return nil, err
		}
		params = append(params, arg)
		if !p.match(TokCOMMA) {
			break
		}
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("unclosed aggregate argument list")
	}
	return params, nil
}

// writeAggregateCombinator writes an aggregate by its bare name -- the
// reference's `.name`, which drops any quoting -- with a parametric one's
// second argument list after the first.
func (g *generator) writeAggregateCombinator(e *Expression) string {
	name := e.Name()
	if id, _ := e.Args["this"].(*Expression); id != nil {
		name = id.Name()
	}
	if !g.tables.KeepsFunctionCase {
		name = strings.ToUpper(name)
	}
	out := name + "(" + g.callList(e) + ")"
	if params, _ := e.Args["params"].([]*Expression); params != nil {
		parts := make([]string, len(params))
		for i, param := range params {
			parts[i] = g.node(param)
		}
		out += "(" + strings.Join(parts, ", ") + ")"
	}
	return out
}

// clickhouseTableRest is ClickHouse's own `_parse_table` around the base
// one: a generate_series aliased without columns names its one column
// after itself, and a trailing FINAL wraps whatever was read.
func (p *parser) clickhouseTableRest(table *Expression) *Expression {
	if table != nil && table.Class == "Table" {
		inner := table.This()
		alias, _ := table.Args["alias"].(*Expression)
		if inner != nil && inner.Class == "GenerateSeries" && alias != nil && alias.Args["columns"] == nil {
			alias.Set("columns", []*Expression{
				New("Identifier", Arg{"this", "generate_series"}, Arg{"quoted", false})})
		}
	}
	if p.match(TokFINAL) {
		return New("Final", Arg{"this", table})
	}
	return table
}

// replaceChild puts `with` where `old` sits among parent's arguments -- the
// reference's `replace`.
func replaceChild(parent, old, with *Expression) {
	for _, key := range parent.Keys {
		switch v := parent.Args[key].(type) {
		case *Expression:
			if v == old {
				parent.Set(key, with)
				return
			}
		case []*Expression:
			for i, item := range v {
				if item == old {
					v[i] = with
					with.Parent = parent
					return
				}
			}
		}
	}
}

// isMapKeyType reports whether a type is the KEY of a MAP type, which
// ClickHouse requires to be non-nullable.
func isMapKeyType(e *Expression) bool {
	parent := e.Parent
	if parent == nil || parent.Class != "DataType" {
		return false
	}
	if kind, _ := parent.Args["this"].(DataTypeKind); kind != "MAP" {
		return false
	}
	// The reference asks for an index of None or 0: a type held outside the
	// list counts as the key too.
	items, _ := parent.Args["expressions"].([]*Expression)
	for i, item := range items {
		if item == e {
			return i == 0
		}
	}
	return true
}
