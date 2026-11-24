package sqlglot

import "strings"

// SourceRange holds a half-open range of rune offsets in a migration script.
type SourceRange struct{ Start, End int }

type ScriptInputError struct {
	Range   SourceRange
	Message string
}

func (e *ScriptInputError) Error() string { return e.Message }

func (e *Expression) SourceRange() (SourceRange, bool) {
	if e == nil || e.sourceRange == nil {
		return SourceRange{}, false
	}
	return *e.sourceRange, true
}

type sourceCall struct {
	name      string
	arguments int
	first     string
	ordered   bool
	quoted    bool
}

// SourceFunction returns the name written at a function call parsed in a
// migration script, before the parser maps it to a dialect-neutral class.
func (e *Expression) SourceFunction() (string, bool) {
	if e == nil || e.sourceCall == nil {
		return "", false
	}
	return e.sourceCall.name, true
}

func (p *parser) locate(expression *Expression, start int) {
	if p.migration && expression != nil && p.index > start {
		expression.sourceRange = &SourceRange{Start: p.tokens[start].Start, End: p.tokens[p.index-1].End + 1}
	}
}

func (p *parser) locateCall(start int) *sourceCall {
	token := p.tokens[start]
	call := &sourceCall{name: token.Text, quoted: token.Type == TokIDENTIFIER}
	if start+2 >= len(p.tokens) || p.tokens[start+1].Type != TokL_PAREN || p.tokens[start+2].Type == TokR_PAREN {
		return call
	}
	call.arguments, call.first = 1, strings.ToUpper(p.tokens[start+2].Text)
	nested := 0
	for i := start + 2; i < p.index; i++ {
		switch p.tokens[i].Type {
		case TokL_PAREN:
			nested++
		case TokR_PAREN:
			if nested == 0 {
				call.ordered = i+2 < len(p.tokens) && strings.EqualFold(p.tokens[i+1].Text, "WITHIN") && strings.EqualFold(p.tokens[i+2].Text, "GROUP")
				return call
			}
			nested--
		case TokCOMMA:
			if nested == 0 {
				call.arguments++
			}
		}
	}
	return call
}

// CompatibilityFeature is a SQL Server feature governed by the database's
// compatibility level under default database-scoped settings.
type CompatibilityFeature struct {
	Name         string
	MinimumLevel int
	Start        int
}

func MigrationFeatures(statement *Expression) []CompatibilityFeature {
	var features []CompatibilityFeature
	statement.Walk(func(e *Expression) bool {
		call := e.sourceCall
		if call == nil || call.quoted || qualifiedCall(e) {
			return true
		}
		name := strings.ToUpper(call.name)
		if level := minimumCompatibility(name, call); level > 0 {
			features = append(features, CompatibilityFeature{Name: name, MinimumLevel: level, Start: e.sourceRange.Start})
		}
		return true
	})
	return features
}

func qualifiedCall(e *Expression) bool {
	switch parent := e.Parent; {
	case parent == nil:
		return false
	case parent.Class == "Dot":
		return parent.Args["expression"] == e
	case parent.Class == "Table" && parent.This() == e:
		return parent.Args["db"] != nil || parent.Args["catalog"] != nil
	default:
		return false
	}
}

func minimumCompatibility(name string, call *sourceCall) int {
	switch name {
	case "STRING_AGG":
		if call.ordered {
			return 110
		}
	case "OPENJSON", "STRING_SPLIT":
		return 130
	case "GENERATE_SERIES":
		return 160
	case "TRIM":
		switch call.first {
		case "LEADING", "TRAILING", "BOTH":
			return 160
		}
	case "LTRIM", "RTRIM":
		if call.arguments == 2 {
			return 160
		}
	case "REGEXP_LIKE":
		return 170
	}
	return 0
}

func (p *parser) parsePostgresDo() (*Expression, error) {
	p.advance()
	var language *Expression
	if p.atWords("LANGUAGE") {
		p.advance()
		var err error
		language, err = p.parseIdentifier()
		if err != nil {
			return nil, err
		}
	}
	token := p.curr()
	if token == nil || !p.atAny(TokSTRING, TokHEREDOC_STRING) {
		return nil, p.unsupported("DO without a quoted body")
	}
	body := New("Literal", Arg{"this", token.Text}, Arg{"is_string", true})
	start := p.index
	p.advance()
	p.locate(body, start)
	if language == nil && p.atWords("LANGUAGE") {
		p.advance()
		var err error
		language, err = p.parseIdentifier()
		if err != nil {
			return nil, err
		}
	}
	return New("Do", Arg{"this", body}, Arg{"language", language}), nil
}
