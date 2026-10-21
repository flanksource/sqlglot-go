package sqlglot

import (
	"slices"
	"strings"
)

func (p *parser) atTSQLTrigger() bool {
	if p.dialect != "tsql" || !p.nativeTSQLModules {
		return false
	}
	name := p.index + 2
	switch {
	case p.atWords("CREATE", "OR", "ALTER", "TRIGGER"):
		name += 2
	case p.atWords("CREATE", "TRIGGER"), p.atWords("ALTER", "TRIGGER"):
	default:
		return false
	}
	for name+1 < len(p.tokens) && p.tokens[name+1].Type == TokDOT {
		name += 2
	}
	// Other dialects' timing-before-ON grammar remains an opaque command.
	return name+1 < len(p.tokens) && p.tokens[name+1].Type == TokON
}

func (p *parser) parseTSQLTrigger() (*Expression, error) {
	alter := p.atWords("ALTER")
	p.advance()
	replace := false
	if p.atWords("OR", "ALTER") {
		p.advance()
		p.advance()
		replace = true
	}
	p.advance()
	name, err := p.parseTableName()
	if err != nil {
		return nil, err
	}
	if !p.match(TokON) {
		return nil, p.unsupported("a trigger without ON and its table")
	}
	target, err := p.parseTableName()
	if err != nil {
		return nil, err
	}
	var options []*Expression
	if p.at(TokWITH) {
		options, err = p.parseProcedureOptions()
		if err != nil {
			return nil, err
		}
	}
	timing := ""
	switch {
	case p.atWords("INSTEAD", "OF"):
		p.advance()
		p.advance()
		timing = "INSTEAD OF"
	case p.atWords("FOR"), p.atWords("AFTER"):
		timing = strings.ToUpper(p.curr().Text)
		p.advance()
	default:
		return nil, p.unsupported("a trigger without FOR, AFTER or INSTEAD OF")
	}
	var events []string
	for {
		if !p.atWords("INSERT") && !p.atWords("UPDATE") && !p.atWords("DELETE") {
			return nil, p.unsupported("an invalid trigger event")
		}
		event := strings.ToUpper(p.curr().Text)
		if slices.Contains(events, event) {
			return nil, p.unsupported("a repeated trigger event")
		}
		events = append(events, event)
		p.advance()
		if !p.match(TokCOMMA) {
			break
		}
	}
	replication := p.atWords("NOT", "FOR", "REPLICATION")
	if replication {
		p.advance()
		p.advance()
		p.advance()
	}
	if !p.match(TokALIAS) {
		return nil, p.unsupported("a trigger without AS and its body")
	}
	begin := p.at(TokBEGIN) && p.opensBlockAt(p.index)
	if begin {
		p.advance()
	}
	body, err := p.parseTSQLStatements(begin)
	if err != nil {
		return nil, err
	}
	return New("Create", Arg{"this", name}, Arg{"kind", "TRIGGER"}, Arg{"tsql_trigger", true}, Arg{"target", target}, Arg{"timing", timing}, Arg{"events", events}, Arg{"alter", alter}, Arg{"replace", replace}, Arg{"begin", begin}, Arg{"properties", propertiesOf(options)}, Arg{"not_for_replication", replication}, Arg{"expression", body}), nil
}

func (g *generator) writeTSQLTrigger(e *Expression) string {
	if g.dialect != "tsql" {
		return g.fail("a T-SQL trigger in another dialect")
	}
	verb := "CREATE"
	if e.Args["alter"] == true {
		verb = "ALTER"
	} else if e.Args["replace"] == true {
		verb = "CREATE OR ALTER"
	}
	timing, _ := e.Args["timing"].(string)
	events, _ := e.Args["events"].([]string)
	if timing == "" || len(events) == 0 || e.Args["target"] == nil || e.Args["expression"] == nil {
		return g.fail("an incomplete T-SQL trigger")
	}
	body := g.child(e, "expression")
	if e.Args["begin"] == true {
		body = "BEGIN " + body
	}
	header := verb + " TRIGGER " + g.child(e, "this") + " ON " + g.child(e, "target")
	if options, _ := e.Args["properties"].(*Expression); options != nil {
		header += " " + g.list(options)
	}
	header += " " + timing + " " + strings.Join(events, ", ")
	if e.Args["not_for_replication"] == true {
		header += " NOT FOR REPLICATION"
	}
	return header + " AS " + body
}
