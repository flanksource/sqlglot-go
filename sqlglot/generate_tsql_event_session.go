package sqlglot

import "strings"

// Writers for the Extended Events session statements parse_tsql_event_session.go
// reads. Names are Identifiers and values Literals, Nationals or Vars, so a
// caller that builds the tree leaves quoting and escaping to the writers
// below; a Var is written as it stands, so one that is not a single word is
// refused rather than written.

func (g *generator) writeCreateEventSession(e *Expression) string {
	if events, _ := e.Args["events"].([]*Expression); len(events) == 0 {
		return g.fail("a CREATE EVENT SESSION with no event")
	}
	return g.eventSessionHead("CREATE", e) + g.eventSessionChanges(e, false)
}

func (g *generator) writeAlterEventSession(e *Expression) string {
	head := g.eventSessionHead("ALTER", e)
	if state, ok := e.Args["state"].(string); ok {
		if state != "START" && state != "STOP" {
			return g.fail("an event session STATE other than START or STOP")
		}
		return head + " STATE = " + state
	}
	changes := g.eventSessionChanges(e, true)
	if changes == "" {
		return g.fail("an ALTER EVENT SESSION that changes nothing")
	}
	return head + changes
}

func (g *generator) writeDropEventSession(e *Expression) string {
	return g.eventSessionHead("DROP", e)
}

// eventSessionHead writes `<verb> EVENT SESSION <name> ON <scope>`.
func (g *generator) eventSessionHead(verb string, e *Expression) string {
	if g.dialect != "tsql" {
		return g.fail("an EVENT SESSION, which only T-SQL writes")
	}
	scope, _ := e.Args["scope"].(string)
	if scope != "SERVER" && scope != "DATABASE" {
		return g.fail("an event session scope other than SERVER or DATABASE")
	}
	name := g.child(e, "this")
	if name == "" {
		return g.fail("an event session without a name")
	}
	return verb + " EVENT SESSION " + name + " ON " + scope
}

// eventSessionChanges writes the events, the targets and the WITH options,
// each list separated by commas and the lists by a space.
func (g *generator) eventSessionChanges(e *Expression, allowDrop bool) string {
	out := ""
	for _, key := range []string{"events", "targets"} {
		items, _ := e.Args[key].([]*Expression)
		for _, item := range items {
			if item.Args["drop"] == true && !allowDrop {
				return g.fail("a DROP EVENT or DROP TARGET in a CREATE EVENT SESSION")
			}
		}
		if len(items) > 0 {
			out += " " + g.joined(items)
		}
	}
	if options, _ := e.Args["options"].([]*Expression); len(options) > 0 {
		out += " WITH (" + g.joined(options) + ")"
	}
	return out
}

// writeEventSessionEvent writes `ADD EVENT <package>.<name> ([SET ...]
// [ACTION (...)] [WHERE (<predicate>)])`. The predicate is written as it
// stands: an XE predicate function is already a condition, and comparing it
// with zero, as T-SQL's writer does with a bare value elsewhere, is not one.
func (g *generator) writeEventSessionEvent(e *Expression) string {
	var parts []string
	if set, _ := e.Args["set"].([]*Expression); len(set) > 0 {
		parts = append(parts, "SET "+g.joined(set))
	}
	if actions, _ := e.Args["actions"].([]*Expression); len(actions) > 0 {
		parts = append(parts, "ACTION ("+g.joined(actions)+")")
	}
	if where, _ := e.Args["where"].(*Expression); where != nil {
		was := g.inEventPredicate
		g.inEventPredicate = true
		parts = append(parts, "WHERE ("+g.node(where)+")")
		g.inEventPredicate = was
	}
	return g.eventSessionItem(e, "EVENT", strings.Join(parts, " "))
}

// writeEventSessionTarget writes `ADD TARGET <package>.<name> [(SET ...)]`.
func (g *generator) writeEventSessionTarget(e *Expression) string {
	options := ""
	if set, _ := e.Args["set"].([]*Expression); len(set) > 0 {
		options = "SET " + g.joined(set)
	}
	return g.eventSessionItem(e, "TARGET", options)
}

// eventSessionItem writes `ADD|DROP <kind> <package>.<name> [(<options>)]`.
// A dropped event or target names it and nothing more.
func (g *generator) eventSessionItem(e *Expression, kind, options string) string {
	pkg, name := g.child(e, "package"), g.child(e, "this")
	if pkg == "" || name == "" {
		return g.fail("an event session " + strings.ToLower(kind) + " without its package and name")
	}
	if e.Args["drop"] == true {
		if options != "" {
			return g.fail("a dropped event session " + strings.ToLower(kind) + " with options")
		}
		return "DROP " + kind + " " + pkg + "." + name
	}
	out := "ADD " + kind + " " + pkg + "." + name
	if options != "" {
		out += " (" + options + ")"
	}
	return out
}

// writeEventSessionOption writes `<name> = <value> [unit]`.
func (g *generator) writeEventSessionOption(e *Expression) string {
	name := e.This()
	if name == nil || name.Class != "Var" || !isBareWord(name.Name()) {
		return g.fail("an event session option whose name is not a word")
	}
	value, _ := e.Args["value"].(*Expression)
	if value == nil {
		return g.fail("an event session option without a value")
	}
	if value.Class == "Var" && !isBareWord(value.Name()) {
		return g.fail("an event session option value that is not a word")
	}
	out := name.Name() + " = " + g.node(value)
	if unit, _ := e.Args["unit"].(string); unit != "" {
		if !eventSessionUnits[unit] {
			return g.fail("an event session option unit other than SECONDS, KB or MB")
		}
		out += " " + unit
	}
	return out
}
