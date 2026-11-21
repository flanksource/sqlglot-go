package scripts

import (
	"fmt"

	"github.com/flanksource/sqlglot-go/sqlglot/access"
)

// analyzeEffects is what running sql does, read by the access package. A
// script's effects are its anonymous statements' plus a write of every
// routine it creates or alters; a created routine's body is stored, not run,
// so its effects are in defines under its name, never the script's. An
// invoked module definition is its body: its effects are the routine's.
// Each statement that could not be read is Unknown as "line N: reason".
func analyzeEffects(sql string, invokedModule bool) (Effects, map[ObjectName]Effects) {
	file, err := access.ReadTSQL(sql)
	if err != nil {
		return Effects{Unknown: []string{err.Error()}}, nil
	}
	effects := Effects{Unknown: unknown(file.Diagnostics)}
	if file.Script != nil {
		addAccesses(&effects, access.Collect(file.Script.Body))
	}
	var defines map[ObjectName]Effects
	for _, routine := range file.Routines {
		if invokedModule {
			addAccesses(&effects, access.Collect(routine.Body))
			continue
		}
		body := Effects{Unknown: unknown(routine.Diagnostics)}
		addAccesses(&body, access.Collect(routine.Body))
		name := objectName(routine.Object)
		effects.Writes = append(effects.Writes, name)
		if _, ok := defines[name]; ok {
			effects.Unknown = append(effects.Unknown, fmt.Sprintf("line %d: routine %s is defined more than once", routine.Line, name))
			continue
		}
		if defines == nil {
			defines = map[ObjectName]Effects{}
		}
		defines[name] = body.Normalized()
	}
	return effects.Normalized(), defines
}

func addAccesses(effects *Effects, accesses []access.Access) {
	for _, a := range accesses {
		name := objectName(a.Object)
		switch a.Kind {
		case access.Read:
			effects.Reads = append(effects.Reads, name)
		case access.Write:
			effects.Writes = append(effects.Writes, name)
		case access.Call:
			effects.Calls = append(effects.Calls, name)
		default:
			panic(fmt.Sprintf("scripts: line %d: access %q to %s is not a read, write or call", a.Line, a.Kind, a.Object))
		}
	}
}

// objectName is the object's name; a name in another database keeps that
// database as a prefix, so it is never taken for the local object.
func objectName(o access.Object) ObjectName {
	if o.Database != "" {
		return NewObjectName(o.Database+"."+o.Schema, o.Name)
	}
	return NewObjectName(o.Schema, o.Name)
}

func unknown(diagnostics []access.Diagnostic) []string {
	var out []string
	for _, d := range diagnostics {
		out = append(out, fmt.Sprintf("line %d: %s", d.Line, d.Message))
	}
	return out
}
