package scripts

import (
	"slices"
	"strings"
)

// ObjectName is a schema-qualified database object a script reads, writes or
// calls: lower-cased "schema.name", with no kind, so a table, view, function
// or procedure of the same name is one object.
type ObjectName string

// NewObjectName builds the canonical name of schema.name.
func NewObjectName(schema, name string) ObjectName {
	return ObjectName(strings.ToLower(strings.TrimSpace(schema) + "." + strings.TrimSpace(name)))
}

// Effects is what a script, or a routine it reaches, does to the database:
// the objects it reads, writes (including routines it creates or alters) and
// calls. Unknown lists the statements whose effects could not be determined,
// as "line N: reason". Analyze reads them from a body with the sqlglot access
// package. A nil Effects means no inferred dependencies.
type Effects struct {
	Reads   []ObjectName `json:"reads,omitempty"`
	Writes  []ObjectName `json:"writes,omitempty"`
	Calls   []ObjectName `json:"calls,omitempty"`
	Unknown []string     `json:"unknown,omitempty"`
}

// Normalized returns e with each list sorted and de-duplicated, so equal
// effects encode, and hash, identically.
func (e Effects) Normalized() Effects {
	norm := func(names []ObjectName) []ObjectName {
		out := slices.Clone(names)
		slices.Sort(out)
		return slices.Compact(out)
	}
	unknown := slices.Clone(e.Unknown)
	slices.Sort(unknown)
	return Effects{Reads: norm(e.Reads), Writes: norm(e.Writes), Calls: norm(e.Calls), Unknown: slices.Compact(unknown)}
}
