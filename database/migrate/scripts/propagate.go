package scripts

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// propagation walks a bundle in order, terraform style: dirty holds every
// object written by a script selected so far, with its latest writer, so a
// script whose dependency closure reaches one is selected as well.
type propagation struct {
	modules map[ObjectName]Effects
	defined map[ObjectName]Effects
	dirty   map[ObjectName]string
}

func newPropagation(modules map[ObjectName]Effects) *propagation {
	return &propagation{modules: modules, defined: map[ObjectName]Effects{}, dirty: map[ObjectName]string{}}
}

// closure is seed plus the reads, writes, calls and unknowns of every routine
// its calls reach: a routine an earlier script in the bundle defines wins over
// the catalog's. A call reaching neither (a builtin, a system procedure, or a
// routine not created yet) contributes only its name.
func (p *propagation) closure(seed Effects) Effects {
	out := Effects{Reads: slices.Clone(seed.Reads), Writes: slices.Clone(seed.Writes), Unknown: slices.Clone(seed.Unknown)}
	visited := map[ObjectName]bool{}
	for queue := slices.Clone(seed.Calls); len(queue) > 0; queue = queue[1:] {
		name := queue[0]
		if visited[name] {
			continue
		}
		visited[name] = true
		out.Calls = append(out.Calls, name)
		routine, ok := p.defined[name]
		if !ok {
			routine, ok = p.modules[name]
		}
		if ok {
			out.Reads = append(out.Reads, routine.Reads...)
			out.Writes = append(out.Writes, routine.Writes...)
			out.Unknown = append(out.Unknown, routine.Unknown...)
			queue = append(queue, routine.Calls...)
		}
	}
	return out.Normalized()
}

// seed is what a script itself depends on and writes: its effects, the
// routines it defines and the objects behind its observed inputs.
func seed(s Script, inputs map[string]string) Effects {
	var e Effects
	if s.Effects != nil {
		e = *s.Effects
		e.Reads, e.Writes, e.Calls = slices.Clone(e.Reads), slices.Clone(e.Writes), slices.Clone(e.Calls)
	}
	for name := range s.Defines {
		e.Writes = append(e.Writes, name)
	}
	for key := range inputs {
		switch kind, object := inputObject(key); kind {
		case "result", "definition":
			e.Calls = append(e.Calls, object)
		case "schema":
			e.Reads = append(e.Reads, object)
		}
	}
	return e
}

// inputObject is the kind and object of an observed input key such as
// "result:dbo.country" or "schema:dbo.table.column"; any other key has no kind.
func inputObject(key string) (string, ObjectName) {
	kind, name, _ := strings.Cut(key, ":")
	parts := strings.Split(name, ".")
	switch kind {
	case "result", "definition", "schema":
		if len(parts) >= 2 {
			return kind, NewObjectName(parts[0], parts[1])
		}
	}
	return "", ""
}

// visit propagates the dirty set into item, settles its selection, and then
// adds its own writes and definitions for the scripts after it.
func (p *propagation) visit(item *Pending) {
	closure := p.closure(seed(item.Script, item.State.Inputs))
	item.EffectsUnknown = closure.Unknown
	reached := append(slices.Clone(closure.Reads), closure.Calls...)
	slices.Sort(reached)
	for _, object := range slices.Compact(reached) {
		if writer, ok := p.dirty[object]; ok {
			item.Reasons = append(item.Reasons, "upstream-write:"+string(object))
			item.Upstream = append(item.Upstream, UpstreamWrite{Object: object, Script: writer})
		}
	}
	for _, key := range slices.Sorted(maps.Keys(item.State.Inputs)) {
		objects := p.observedObjects(key)
		if writers := p.writersOf(objects); len(writers) > 0 {
			item.AfterApply = append(item.AfterApply, AfterApplyInput{Key: key, Writers: writers})
		}
		if slices.ContainsFunc(objects, func(o ObjectName) bool { return slices.Contains(closure.Writes, o) }) {
			item.SelfWritten = append(item.SelfWritten, key)
		}
	}
	item.settle()
	if item.Selected {
		p.write(item.Script.ID, closure.Writes)
	}
	for name, effects := range item.Script.Defines {
		p.defined[name] = effects.Normalized()
	}
}

// write marks objects as written by writer for every script after it.
func (p *propagation) write(writer string, objects []ObjectName) {
	for _, object := range objects {
		p.dirty[object] = writer
	}
}

// observedObjects is what the input key observes: a function result depends
// on everything the function reaches.
func (p *propagation) observedObjects(key string) []ObjectName {
	kind, object := inputObject(key)
	if kind == "" {
		return nil
	}
	objects := []ObjectName{object}
	if kind == "result" {
		c := p.closure(Effects{Calls: objects})
		objects = append(c.Reads, c.Calls...)
	}
	return objects
}

// writersOf names the selected scripts writing any of objects.
func (p *propagation) writersOf(objects []ObjectName) []string {
	var writers []string
	for _, o := range objects {
		if writer, ok := p.dirty[o]; ok {
			writers = append(writers, writer)
		}
	}
	slices.Sort(writers)
	return slices.Compact(writers)
}

func (item *Pending) settle() {
	slices.Sort(item.Reasons)
	item.Reasons = slices.Compact(item.Reasons)
	slices.Sort(item.Blocked)
	item.Selected = len(item.Reasons) > 0 && len(item.Blocked) == 0
}

// effectsHash identifies a script's planned effects; empty when it has none.
func effectsHash(s Script) string {
	if s.Effects == nil && len(s.Defines) == 0 {
		return ""
	}
	var own Effects
	if s.Effects != nil {
		own = s.Effects.Normalized()
	}
	defines := map[ObjectName]Effects{}
	for name, effects := range s.Defines {
		defines[name] = effects.Normalized()
	}
	encoded, err := json.Marshal(struct {
		Effects Effects
		Defines map[ObjectName]Effects
	}{own, defines})
	if err != nil {
		panic(fmt.Sprintf("encode effects of %s: %v", s.ID, err))
	}
	return SourceHash(string(encoded))
}

func unknownWarning(item Pending) string {
	return fmt.Sprintf("script %s has unknown effects (%s); apply re-inspects the plan after it runs", item.Script.ID, strings.Join(item.EffectsUnknown, "; "))
}
