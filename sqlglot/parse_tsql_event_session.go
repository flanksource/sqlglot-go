package sqlglot

import "strings"

// Readers for SQL Server's Extended Events session DDL: CREATE, ALTER and DROP
// EVENT SESSION. The reference has no grammar for any of the three -- it warns
// and keeps the text as a Command -- so the nodes here are the port's own,
// named for what they hold: CreateEventSession, AlterEventSession and
// DropEventSession, made of EventSessionEvent, EventSessionTarget and
// EventSessionOption. A form outside this grammar is refused, never kept as
// text.

// eventSessionUnits are the words a session option's number may carry:
// MAX_DISPATCH_LATENCY in SECONDS, MAX_MEMORY and MAX_EVENT_SIZE in KB or MB.
var eventSessionUnits = map[string]bool{"SECONDS": true, "KB": true, "MB": true}

// atEventSession reports a CREATE, ALTER or DROP of an event session.
func (p *parser) atEventSession() bool {
	return p.dialect == "tsql" && (p.atWords("CREATE", "EVENT", "SESSION") ||
		p.atWords("ALTER", "EVENT", "SESSION") || p.atWords("DROP", "EVENT", "SESSION"))
}

// parseEventSession reads `<verb> EVENT SESSION <name> ON SERVER|DATABASE`
// and whatever the verb takes after it.
func (p *parser) parseEventSession() (*Expression, error) {
	verb := strings.ToUpper(p.curr().Text)
	p.index += 3
	name, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}
	if !p.match(TokON) || !p.atWordsAny("SERVER", "DATABASE") {
		return nil, p.unsupported("an event session not ON SERVER or ON DATABASE")
	}
	scope := strings.ToUpper(p.curr().Text)
	p.advance()
	class := map[string]string{"CREATE": "CreateEventSession", "ALTER": "AlterEventSession", "DROP": "DropEventSession"}[verb]
	node := New(class, Arg{"this", name}, Arg{"scope", scope})
	switch verb {
	case "CREATE":
		if err := p.parseEventSessionChanges(node, false); err != nil {
			return nil, err
		}
		if node.Args["events"] == nil {
			return nil, p.unsupported("an event session with no event")
		}
	case "ALTER":
		return p.parseAlterEventSession(node)
	}
	return node, nil
}

// parseAlterEventSession reads `STATE = START|STOP`, or the events and targets
// an ALTER adds and drops and the options it sets.
func (p *parser) parseAlterEventSession(node *Expression) (*Expression, error) {
	if p.matchWords("STATE") {
		if !p.match(TokEQ) || !p.atWordsAny("START", "STOP") {
			return nil, p.unsupported("an event session STATE other than START or STOP")
		}
		node.Set("state", strings.ToUpper(p.curr().Text))
		p.advance()
		return node, nil
	}
	if err := p.parseEventSessionChanges(node, true); err != nil {
		return nil, err
	}
	if len(node.Keys) == 2 {
		return nil, p.unsupported("an ALTER EVENT SESSION that changes nothing")
	}
	return node, nil
}

// parseEventSessionChanges reads the events, then the targets, then the WITH
// options. Only an ALTER may DROP an event or a target.
func (p *parser) parseEventSessionChanges(node *Expression, allowDrop bool) error {
	for _, kind := range []string{"EVENT", "TARGET"} {
		items, err := p.parseEventSessionList(kind, allowDrop)
		if err != nil {
			return err
		}
		if len(items) > 0 {
			node.Set(strings.ToLower(kind)+"s", items)
		}
	}
	if !p.match(TokWITH) {
		return nil
	}
	options, err := p.parseParenthesisedSessionOptions("")
	if err != nil {
		return err
	}
	node.Set("options", options)
	return nil
}

// parseEventSessionList reads `ADD|DROP <kind> ...` clauses separated by
// commas. A comma is followed by another clause of the same kind.
func (p *parser) parseEventSessionList(kind string, allowDrop bool) ([]*Expression, error) {
	var items []*Expression
	for len(items) == 0 || p.match(TokCOMMA) {
		if !p.atWords("ADD", kind) && (!allowDrop || !p.atWords("DROP", kind)) {
			if len(items) == 0 {
				return nil, nil
			}
			return nil, p.unsupported("an event session list with no " + kind + " after its comma")
		}
		item, err := p.parseEventSessionItem(kind)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// parseEventSessionItem reads one `ADD|DROP EVENT|TARGET <package>.<name>`
// and, for an added one, its parenthesised options.
func (p *parser) parseEventSessionItem(kind string) (*Expression, error) {
	drop := p.atWords("DROP")
	p.index += 2
	pkg, name, err := p.parsePackageObject()
	if err != nil {
		return nil, err
	}
	node := New(map[string]string{"EVENT": "EventSessionEvent", "TARGET": "EventSessionTarget"}[kind],
		Arg{"this", name}, Arg{"package", pkg})
	if drop {
		node.Set("drop", true)
		return node, nil
	}
	if !p.at(TokL_PAREN) {
		return node, nil
	}
	if kind == "TARGET" {
		set, err := p.parseParenthesisedSessionOptions("SET")
		if err != nil {
			return nil, err
		}
		node.Set("set", set)
		return node, nil
	}
	p.advance()
	if err := p.parseSessionEventOptions(node); err != nil {
		return nil, err
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("an event whose options do not close")
	}
	return node, nil
}

// parseSessionEventOptions reads an event's `[SET ...] [ACTION (...)]
// [WHERE <predicate>]`, at least one of them. Parentheses round the whole
// predicate are the clause's, not the condition's: the tree holds the bare
// condition and the writer puts them back.
func (p *parser) parseSessionEventOptions(event *Expression) error {
	if p.match(TokSET) {
		set, err := p.parseEventSessionOptions()
		if err != nil {
			return err
		}
		event.Set("set", set)
	}
	if p.matchWords("ACTION") {
		actions, err := p.parseSessionActions()
		if err != nil {
			return err
		}
		event.Set("actions", actions)
	}
	if p.match(TokWHERE) {
		where, err := p.parseExpression()
		if err != nil {
			return err
		}
		if where.Class == "Paren" {
			where = where.This()
		}
		event.Set("where", where)
	}
	if len(event.Keys) == 2 {
		return p.unsupported("an event with empty options")
	}
	return nil
}

// parseSessionActions reads `(<package>.<action>, ...)`. An action is a field
// the event carries, which a predicate names as a column, so it is a Column.
func (p *parser) parseSessionActions() ([]*Expression, error) {
	if !p.match(TokL_PAREN) {
		return nil, p.unsupported("an ACTION list without parentheses")
	}
	var actions []*Expression
	for len(actions) == 0 || p.match(TokCOMMA) {
		pkg, name, err := p.parsePackageObject()
		if err != nil {
			return nil, err
		}
		actions = append(actions, New("Column", Arg{"this", name}, Arg{"table", pkg}))
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("an ACTION list that does not close")
	}
	return actions, nil
}

// parsePackageObject reads `<package>.<name>`: sqlserver.rpc_completed,
// package0.ring_buffer.
func (p *parser) parsePackageObject() (pkg, name *Expression, err error) {
	if pkg, err = p.parseIdentifier(); err != nil {
		return nil, nil, err
	}
	if !p.match(TokDOT) {
		return nil, nil, p.unsupported("an event session object without its package")
	}
	if name, err = p.parseIdentifier(); err != nil {
		return nil, nil, err
	}
	return pkg, name, nil
}

// parseParenthesisedSessionOptions reads `([word] <option>, ...)`: a target's
// `(SET ...)` or a session's `WITH (...)`.
func (p *parser) parseParenthesisedSessionOptions(word string) ([]*Expression, error) {
	if !p.match(TokL_PAREN) || (word != "" && !p.matchWords(word)) {
		return nil, p.unsupported("event session options not in parentheses")
	}
	options, err := p.parseEventSessionOptions()
	if err != nil {
		return nil, err
	}
	if !p.match(TokR_PAREN) {
		return nil, p.unsupported("event session options that do not close")
	}
	return options, nil
}

// parseEventSessionOptions reads `<name> = <value> [unit]`, separated by
// commas. The name is a Var; a value that is a bare word -- OFF,
// ALLOW_SINGLE_EVENT_LOSS, INFINITE -- is a Var too, and anything else is an
// expression: a number, a string, a parenthesised value.
func (p *parser) parseEventSessionOptions() ([]*Expression, error) {
	var options []*Expression
	for len(options) == 0 || p.match(TokCOMMA) {
		name := p.parseEventSessionWord()
		if name == nil || !p.match(TokEQ) {
			return nil, p.unsupported("an event session option that is not name = value")
		}
		value := p.parseEventSessionWord()
		if value == nil {
			var err error
			if value, err = p.parseExpression(); err != nil {
				return nil, err
			}
		}
		option := New("EventSessionOption", Arg{"this", name}, Arg{"value", value})
		if c := p.curr(); c != nil && c.Type != TokIDENTIFIER && eventSessionUnits[strings.ToUpper(c.Text)] {
			p.advance()
			option.Set("unit", strings.ToUpper(c.Text))
		}
		options = append(options, option)
	}
	return options, nil
}

// parseEventSessionWord reads one bare word as a Var, or nothing.
func (p *parser) parseEventSessionWord() *Expression {
	c := p.curr()
	if c == nil || c.Type == TokIDENTIFIER || stringTokens[c.Type] || !isBareWord(c.Text) {
		return nil
	}
	p.advance()
	return New("Var", Arg{"this", c.Text})
}
