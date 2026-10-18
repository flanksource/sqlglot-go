package sqlglot

import (
	"strings"
	"testing"
)

// SQL Server Extended Events session DDL, which the reference reads only as a
// Command (CREATE and DROP the port refused outright). The port reads it as a
// tree -- the session name an Identifier, each event, action and target a
// node, the predicate an ordinary condition -- so a caller can BUILD the
// statement and leave the quoting to the generator.

const xeActions = "ACTION (package0.event_sequence, sqlserver.client_app_name, sqlserver.client_hostname, " +
	"sqlserver.database_name, sqlserver.session_id, sqlserver.sql_text, sqlserver.username)"

func TestTSQLEventSessionRoundTrips(t *testing.T) {
	for _, c := range []struct{ name, sql, written string }{
		{"ring buffer, predicates, several events and a bare deadlock event",
			"CREATE EVENT SESSION [commons_db_trace_test] ON SERVER\n" +
				"ADD EVENT sqlserver.rpc_completed (\n    " + xeActions + "\n" +
				"    WHERE (duration >= 1000 AND (sqlserver.equal_i_sql_unicode_string(sqlserver.database_name, N'acme')) AND sqlserver.session_id <> 55)\n),\n" +
				"ADD EVENT sqlserver.sql_batch_completed (\n    " + xeActions + "\n),\n" +
				"ADD EVENT sqlserver.xml_deadlock_report\n" +
				"ADD TARGET package0.ring_buffer (SET max_memory = 4096, max_events_limit = 1000)\n" +
				"WITH (MAX_DISPATCH_LATENCY = 1 SECONDS, TRACK_CAUSALITY = OFF, STARTUP_STATE = OFF)",
			"CREATE EVENT SESSION [commons_db_trace_test] ON SERVER " +
				"ADD EVENT sqlserver.rpc_completed (" + xeActions +
				" WHERE (duration >= 1000 AND (sqlserver.equal_i_sql_unicode_string(sqlserver.database_name, N'acme')) AND sqlserver.session_id <> 55)), " +
				"ADD EVENT sqlserver.sql_batch_completed (" + xeActions + "), " +
				"ADD EVENT sqlserver.xml_deadlock_report " +
				"ADD TARGET package0.ring_buffer (SET max_memory = 4096, max_events_limit = 1000) " +
				"WITH (MAX_DISPATCH_LATENCY = 1 SECONDS, TRACK_CAUSALITY = OFF, STARTUP_STATE = OFF)"},
		{"event file, object event SET, wildcard and exclusion predicates",
			"CREATE EVENT SESSION [trace] ON SERVER ADD EVENT sqlserver.object_created (\n" +
				"    SET collect_database_name = (1)\n    ACTION (package0.event_sequence, sqlserver.sql_text)\n" +
				"    WHERE (ddl_phase = 1 AND database_id <> 2 AND object_type <> 21587 AND " +
				"(sqlserver.like_i_sql_unicode_string(sqlserver.client_app_name, N'acme%') OR sqlserver.equal_i_sql_unicode_string(sqlserver.client_app_name, N'o''brien')) AND " +
				"(NOT (sqlserver.like_i_sql_unicode_string(sqlserver.client_hostname, N'%[_]tmp'))))\n)\n" +
				`ADD TARGET package0.event_file (SET filename = N'D:\rdsdbdata\log\s.xel', max_file_size = 256, max_rollover_files = 10)` + "\n" +
				"WITH (MAX_DISPATCH_LATENCY = 1 SECONDS, TRACK_CAUSALITY = ON, STARTUP_STATE = OFF)",
			"CREATE EVENT SESSION [trace] ON SERVER ADD EVENT sqlserver.object_created (" +
				"SET collect_database_name = (1) ACTION (package0.event_sequence, sqlserver.sql_text) " +
				"WHERE (ddl_phase = 1 AND database_id <> 2 AND object_type <> 21587 AND " +
				"(sqlserver.like_i_sql_unicode_string(sqlserver.client_app_name, N'acme%') OR sqlserver.equal_i_sql_unicode_string(sqlserver.client_app_name, N'o''brien')) AND " +
				"(NOT (sqlserver.like_i_sql_unicode_string(sqlserver.client_hostname, N'%[_]tmp'))))) " +
				`ADD TARGET package0.event_file (SET filename = N'D:\rdsdbdata\log\s.xel', max_file_size = 256, max_rollover_files = 10) ` +
				"WITH (MAX_DISPATCH_LATENCY = 1 SECONDS, TRACK_CAUSALITY = ON, STARTUP_STATE = OFF)"},
		{"unparenthesised predicate, two targets, every option form",
			"create event session x on server add event sqlserver.error_reported (where severity >= 16) " +
				"add target package0.ring_buffer, add target package0.event_counter " +
				"with (max_memory = 4096 KB, event_retention_mode = allow_single_event_loss, max_dispatch_latency = infinite, max_event_size = 0 mb)",
			"CREATE EVENT SESSION x ON SERVER ADD EVENT sqlserver.error_reported (WHERE (severity >= 16)) " +
				"ADD TARGET package0.ring_buffer, ADD TARGET package0.event_counter " +
				"WITH (max_memory = 4096 KB, event_retention_mode = allow_single_event_loss, max_dispatch_latency = infinite, max_event_size = 0 MB)"},
		{"database scope",
			"CREATE EVENT SESSION x ON DATABASE ADD EVENT sqlserver.rpc_completed",
			"CREATE EVENT SESSION x ON DATABASE ADD EVENT sqlserver.rpc_completed"},
		{"start", "ALTER EVENT SESSION [trace] ON SERVER STATE = START", "ALTER EVENT SESSION [trace] ON SERVER STATE = START"},
		{"stop", "alter event session [trace] on server state = stop", "ALTER EVENT SESSION [trace] ON SERVER STATE = STOP"},
		{"replace an event's predicate",
			"ALTER EVENT SESSION [trace] ON SERVER DROP EVENT sqlserver.rpc_completed",
			"ALTER EVENT SESSION [trace] ON SERVER DROP EVENT sqlserver.rpc_completed"},
		{"add an event",
			"ALTER EVENT SESSION [trace] ON SERVER ADD EVENT sqlserver.rpc_completed (ACTION (package0.event_sequence) WHERE (sqlserver.session_id <> 56))",
			"ALTER EVENT SESSION [trace] ON SERVER ADD EVENT sqlserver.rpc_completed (ACTION (package0.event_sequence) WHERE (sqlserver.session_id <> 56))"},
		{"drop a target and set an option",
			"ALTER EVENT SESSION [trace] ON SERVER DROP TARGET package0.ring_buffer WITH (STARTUP_STATE = ON)",
			"ALTER EVENT SESSION [trace] ON SERVER DROP TARGET package0.ring_buffer WITH (STARTUP_STATE = ON)"},
		{"drop", "DROP EVENT SESSION [trace] ON SERVER", "DROP EVENT SESSION [trace] ON SERVER"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tree, err := ParseOne(c.sql, "tsql")
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !IsWrite(tree) {
				t.Fatalf("%s is not a write", tree.Class)
			}
			written, err := Generate(tree, "tsql")
			if err != nil || written != c.written {
				t.Fatalf("wrote %q (%v)\n           want %q", written, err, c.written)
			}
			again, err := ParseOne(written, "tsql")
			if err != nil || !again.Equal(tree) {
				t.Fatalf("not a fixed point (%v)", err)
			}
		})
	}
}

// The tree the ring-buffer form reads into: every part a node a caller could
// have built, and the predicate an ordinary condition.
func TestTSQLEventSessionNodes(t *testing.T) {
	tree, err := ParseOne("CREATE EVENT SESSION [s] ON SERVER ADD EVENT sqlserver.rpc_completed "+
		"(ACTION (sqlserver.sql_text) WHERE (duration >= 5 AND sqlserver.session_id <> 55)) "+
		"ADD TARGET package0.ring_buffer (SET max_memory = 4096) WITH (MAX_DISPATCH_LATENCY = 1 SECONDS)", "tsql")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	tree.Walk(func(n *Expression) bool {
		switch n.Class {
		case "Identifier", "Var":
			got = append(got, n.Class+":"+n.Name())
		case "Literal", "Paren":
		default:
			got = append(got, n.Class)
		}
		return true
	})
	want := "CreateEventSession Identifier:s EventSessionEvent Identifier:rpc_completed Identifier:sqlserver " +
		"Column Identifier:sql_text Identifier:sqlserver " +
		"And GTE Column Identifier:duration NEQ Column Identifier:session_id Identifier:sqlserver " +
		"EventSessionTarget Identifier:ring_buffer Identifier:package0 EventSessionOption Var:max_memory " +
		"EventSessionOption Var:MAX_DISPATCH_LATENCY"
	if strings.Join(got, " ") != want {
		t.Fatalf("tree is\n%s\nwant\n%s", strings.Join(got, " "), want)
	}
	if tree.Args["scope"] != "SERVER" {
		t.Fatalf("scope is %v", tree.Args["scope"])
	}
	option := tree.Args["options"].([]*Expression)[0]
	if option.Args["unit"] != "SECONDS" {
		t.Fatalf("unit is %v", option.Args["unit"])
	}
}

// Built from nodes alone, with a session name holding the bracket that closes
// a T-SQL name and a path and a predicate value holding the quote that closes
// a string: the generator quotes and escapes both, and the result reads back
// as the tree it was written from.
func TestTSQLEventSessionBuiltFromNodes(t *testing.T) {
	ident := func(name string) *Expression { return New("Identifier", Arg{"this", name}, Arg{"quoted", false}) }
	number := func(n string) *Expression { return New("Literal", Arg{"this", n}, Arg{"is_string", false}) }
	option := func(name string, value *Expression, unit ...Arg) *Expression {
		return New("EventSessionOption", append([]Arg{{"this", New("Var", Arg{"this", name})}, {"value", value}}, unit...)...)
	}
	action := func(pkg, name string) *Expression {
		return New("Column", Arg{"this", ident(name)}, Arg{"table", ident(pkg)})
	}
	predicate := New("And",
		Arg{"this", New("GTE", Arg{"this", New("Column", Arg{"this", ident("duration")})}, Arg{"expression", number("1000")})},
		Arg{"expression", New("Dot",
			Arg{"this", ident("sqlserver")},
			Arg{"expression", New("Anonymous",
				Arg{"this", "equal_i_sql_unicode_string"},
				Arg{"expressions", []*Expression{action("sqlserver", "database_name"), New("National", Arg{"this", "o'brien"})}})})})
	event := New("EventSessionEvent",
		Arg{"this", ident("rpc_completed")}, Arg{"package", ident("sqlserver")},
		Arg{"actions", []*Expression{action("package0", "event_sequence"), action("sqlserver", "sql_text")}},
		Arg{"where", predicate})
	target := New("EventSessionTarget",
		Arg{"this", ident("event_file")}, Arg{"package", ident("package0")},
		Arg{"set", []*Expression{
			option("filename", New("National", Arg{"this", `C:\xe\it's.xel`})),
			option("max_file_size", number("256")),
		}})
	session := New("CreateEventSession",
		Arg{"this", New("Identifier", Arg{"this", "trace]x"}, Arg{"quoted", true})},
		Arg{"scope", "SERVER"},
		Arg{"events", []*Expression{event}},
		Arg{"targets", []*Expression{target}},
		Arg{"options", []*Expression{
			option("MAX_DISPATCH_LATENCY", number("1"), Arg{"unit", "SECONDS"}),
			option("STARTUP_STATE", New("Var", Arg{"this", "OFF"})),
		}})

	written, err := Generate(session, "tsql")
	want := "CREATE EVENT SESSION [trace]]x] ON SERVER ADD EVENT sqlserver.rpc_completed " +
		"(ACTION (package0.event_sequence, sqlserver.sql_text) " +
		"WHERE (duration >= 1000 AND sqlserver.equal_i_sql_unicode_string(sqlserver.database_name, N'o''brien'))) " +
		`ADD TARGET package0.event_file (SET filename = N'C:\xe\it''s.xel', max_file_size = 256) ` +
		"WITH (MAX_DISPATCH_LATENCY = 1 SECONDS, STARTUP_STATE = OFF)"
	if err != nil || written != want {
		t.Fatalf("wrote %q (%v)\n           want %q", written, err, want)
	}
	read, err := ParseOne(written, "tsql")
	if err != nil || !read.Equal(session) {
		t.Fatalf("read back as a different tree (%v)", err)
	}

	for _, statement := range []struct {
		node *Expression
		want string
	}{
		{New("AlterEventSession", Arg{"this", ident("trace")}, Arg{"scope", "SERVER"}, Arg{"state", "START"}),
			"ALTER EVENT SESSION trace ON SERVER STATE = START"},
		{New("DropEventSession", Arg{"this", New("Identifier", Arg{"this", "a b"}, Arg{"quoted", true})}, Arg{"scope", "SERVER"}),
			"DROP EVENT SESSION [a b] ON SERVER"},
	} {
		if written, err := Generate(statement.node, "tsql"); err != nil || written != statement.want {
			t.Fatalf("wrote %q (%v), want %q", written, err, statement.want)
		}
	}
}

func TestTSQLEventSessionRefusals(t *testing.T) {
	for _, c := range []struct{ name, sql string }{
		{"no event", "CREATE EVENT SESSION x ON SERVER"},
		{"no ON", "DROP EVENT SESSION x"},
		{"unknown scope", "DROP EVENT SESSION x ON INSTANCE"},
		{"DROP EVENT in a CREATE", "CREATE EVENT SESSION x ON SERVER DROP EVENT sqlserver.a"},
		{"event without a package", "CREATE EVENT SESSION x ON SERVER ADD EVENT rpc_completed"},
		{"ADD without EVENT or TARGET", "CREATE EVENT SESSION x ON SERVER ADD sqlserver.a"},
		{"empty event options", "CREATE EVENT SESSION x ON SERVER ADD EVENT sqlserver.a ()"},
		{"unknown event clause", "CREATE EVENT SESSION x ON SERVER ADD EVENT sqlserver.a (ACTION (sqlserver.b) FROB)"},
		{"unclosed ACTION list", "CREATE EVENT SESSION x ON SERVER ADD EVENT sqlserver.a (ACTION (sqlserver.b"},
		{"action without a package", "CREATE EVENT SESSION x ON SERVER ADD EVENT sqlserver.a (ACTION (sql_text))"},
		{"bad predicate", "CREATE EVENT SESSION x ON SERVER ADD EVENT sqlserver.a (WHERE (a >=))"},
		{"options on a dropped event", "ALTER EVENT SESSION x ON SERVER DROP EVENT sqlserver.a (ACTION (sqlserver.b))"},
		{"comma before a target", "CREATE EVENT SESSION x ON SERVER ADD EVENT sqlserver.a, ADD TARGET package0.ring_buffer"},
		{"event name missing after the dot", "CREATE EVENT SESSION x ON SERVER ADD EVENT sqlserver."},
		{"package missing", "CREATE EVENT SESSION x ON SERVER ADD EVENT .a"},
		{"session name missing", "DROP EVENT SESSION ON SERVER"},
		{"bad event SET", "CREATE EVENT SESSION x ON SERVER ADD EVENT sqlserver.a (SET = 1)"},
		{"ACTION without parentheses", "CREATE EVENT SESSION x ON SERVER ADD EVENT sqlserver.a (ACTION sqlserver.b)"},
		{"bad target SET", "CREATE EVENT SESSION x ON SERVER ADD EVENT sqlserver.a ADD TARGET package0.ring_buffer (SET max_memory = 1"},
		{"bad WITH option", "CREATE EVENT SESSION x ON SERVER ADD EVENT sqlserver.a WITH (= 1)"},
		{"bad event in an ALTER", "ALTER EVENT SESSION x ON SERVER ADD EVENT sqlserver."},
		{"bad target in a list", "CREATE EVENT SESSION x ON SERVER ADD EVENT sqlserver.a ADD TARGET package0."},
		{"target without SET", "CREATE EVENT SESSION x ON SERVER ADD EVENT sqlserver.a ADD TARGET package0.ring_buffer (max_memory = 1)"},
		{"option without a value", "CREATE EVENT SESSION x ON SERVER ADD EVENT sqlserver.a WITH (MAX_MEMORY =)"},
		{"option without =", "CREATE EVENT SESSION x ON SERVER ADD EVENT sqlserver.a WITH (MAX_MEMORY 4)"},
		{"unknown unit", "CREATE EVENT SESSION x ON SERVER ADD EVENT sqlserver.a WITH (MAX_MEMORY = 4 TB)"},
		{"WITH without parentheses", "CREATE EVENT SESSION x ON SERVER ADD EVENT sqlserver.a WITH MAX_MEMORY = 4"},
		{"unknown state", "ALTER EVENT SESSION x ON SERVER STATE = PAUSE"},
		{"STATE without =", "ALTER EVENT SESSION x ON SERVER STATE START"},
		{"ALTER that changes nothing", "ALTER EVENT SESSION x ON SERVER"},
		{"trailing tokens", "DROP EVENT SESSION x ON SERVER CASCADE"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if tree, err := ParseOne(c.sql, "tsql"); err == nil {
				written, _ := Generate(tree, "tsql")
				t.Fatalf("accepted as %s: %s", tree.Class, written)
			}
		})
	}
}

// A built tree the generator cannot write faithfully is refused, never
// written approximately: a Var is written as it stands, so one that is not a
// word could carry any SQL at all.
func TestTSQLEventSessionGenerateRefusals(t *testing.T) {
	name := New("Identifier", Arg{"this", "s"}, Arg{"quoted", false})
	event := func(args ...Arg) *Expression {
		return New("EventSessionEvent", append([]Arg{{"this", New("Identifier", Arg{"this", "a"})}, {"package", New("Identifier", Arg{"this", "sqlserver"})}}, args...)...)
	}
	create := func(options ...*Expression) *Expression {
		return New("CreateEventSession", Arg{"this", name}, Arg{"scope", "SERVER"},
			Arg{"events", []*Expression{event()}}, Arg{"options", options})
	}
	option := func(name string, value *Expression, unit ...Arg) *Expression {
		return New("EventSessionOption", append([]Arg{{"this", New("Var", Arg{"this", name})}, {"value", value}}, unit...)...)
	}
	word := func(w string) *Expression { return New("Var", Arg{"this", w}) }
	for _, c := range []struct {
		name    string
		node    *Expression
		dialect string
	}{
		{"another dialect", create(), "duckdb"},
		{"no session name", New("DropEventSession", Arg{"scope", "SERVER"}), "tsql"},
		{"unknown scope", New("DropEventSession", Arg{"this", name}, Arg{"scope", "SERVER; DROP TABLE t"}), "tsql"},
		{"no event", New("CreateEventSession", Arg{"this", name}, Arg{"scope", "SERVER"}), "tsql"},
		{"dropped event in a CREATE", New("CreateEventSession", Arg{"this", name}, Arg{"scope", "SERVER"},
			Arg{"events", []*Expression{event(Arg{"drop", true})}}), "tsql"},
		{"dropped event with actions", New("AlterEventSession", Arg{"this", name}, Arg{"scope", "SERVER"},
			Arg{"events", []*Expression{event(Arg{"drop", true}, Arg{"actions", []*Expression{New("Column", Arg{"this", name})}})}}), "tsql"},
		{"dropped target with SET", New("AlterEventSession", Arg{"this", name}, Arg{"scope", "SERVER"},
			Arg{"targets", []*Expression{New("EventSessionTarget", Arg{"this", name}, Arg{"package", name}, Arg{"drop", true},
				Arg{"set", []*Expression{option("max_memory", word("x"))}})}}), "tsql"},
		{"event without a package", New("CreateEventSession", Arg{"this", name}, Arg{"scope", "SERVER"},
			Arg{"events", []*Expression{New("EventSessionEvent", Arg{"this", name})}}), "tsql"},
		{"unknown state", New("AlterEventSession", Arg{"this", name}, Arg{"scope", "SERVER"}, Arg{"state", "PAUSE"}), "tsql"},
		{"ALTER that changes nothing", New("AlterEventSession", Arg{"this", name}, Arg{"scope", "SERVER"}), "tsql"},
		{"option name that is not a word", create(option("MAX_MEMORY = 1); DROP TABLE t; --", word("1"))), "tsql"},
		{"option value that is not a word", create(option("STARTUP_STATE", word("OFF); DROP TABLE t; --"))), "tsql"},
		{"option without a value", create(option("STARTUP_STATE", nil)), "tsql"},
		{"unknown unit", create(option("MAX_MEMORY", New("Literal", Arg{"this", "4"}, Arg{"is_string", false}), Arg{"unit", "TB"})), "tsql"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if written, err := Generate(c.node, c.dialect); err == nil {
				t.Fatalf("wrote %q", written)
			}
		})
	}
}
