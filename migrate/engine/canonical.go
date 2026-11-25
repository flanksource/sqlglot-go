package engine

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/flanksource/sqlglot-go/migrate"
)

func compareName(a, b migrate.Name) int {
	return cmp.Or(cmp.Compare(a.Schema, b.Schema), cmp.Compare(a.Name, b.Name))
}

func compareColumnRef(a, b ColumnRef) int {
	return cmp.Or(compareName(a.Table, b.Table), cmp.Compare(a.Column, b.Column))
}

// memberOf is the change member of a dependent: the view and table kinds are
// whole objects and have none.
func memberOf(d Dependent) string {
	if d.Kind == DependentView || d.Kind == DependentTable {
		return ""
	}
	return d.Name
}

func compareDependent(a, b Dependent) int {
	return cmp.Or(cmp.Compare(a.Kind, b.Kind), compareName(a.Table, b.Table), cmp.Compare(memberOf(a), memberOf(b)))
}

// clone deep-copies a JSON-shaped value without changing how it marshals.
func clone[T any](v T) (T, error) {
	var out T
	body, err := json.Marshal(v)
	if err != nil {
		return out, fmt.Errorf("engine: copy %T: %w", v, err)
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("engine: copy %T: %w", v, err)
	}
	return out, nil
}

// canonicalSchema returns a copy of s with every set-valued member sorted.
// Column and index-column order is significant and kept.
func canonicalSchema(s migrate.Schema) (migrate.Schema, error) {
	s, err := clone(s)
	if err != nil {
		return s, err
	}
	if s.Tables == nil {
		s.Tables = []migrate.Table{}
	}
	if s.Definitions == nil {
		s.Definitions = []migrate.Definition{}
	}
	slices.SortFunc(s.Tables, func(a, b migrate.Table) int { return compareName(a.Name, b.Name) })
	slices.SortFunc(s.Definitions, func(a, b migrate.Definition) int { return compareName(a.Name, b.Name) })
	slices.SortFunc(s.Sequences, func(a, b migrate.Sequence) int { return compareName(a.Name, b.Name) })
	slices.SortFunc(s.TableTypes, func(a, b migrate.TableType) int { return compareName(a.Name, b.Name) })
	for i := range s.Tables {
		t := &s.Tables[i]
		slices.SortFunc(t.Indexes, func(a, b migrate.Index) int { return cmp.Compare(a.Name, b.Name) })
		slices.SortFunc(t.ForeignKeys, func(a, b migrate.ForeignKey) int { return cmp.Compare(a.Name, b.Name) })
		slices.SortFunc(t.Checks, func(a, b migrate.Check) int { return cmp.Compare(a.Name, b.Name) })
		for j := range t.Indexes {
			slices.Sort(t.Indexes[j].Included)
		}
	}
	for i := range s.Definitions {
		slices.SortFunc(s.Definitions[i].Dependencies, compareName)
		slices.SortFunc(s.Definitions[i].RuntimeDependencies, func(a, b migrate.RuntimeDependency) int {
			return cmp.Or(cmp.Compare(a.Server, b.Server), cmp.Compare(a.Database, b.Database), cmp.Compare(a.Schema, b.Schema), cmp.Compare(a.Name, b.Name))
		})
	}
	return s, nil
}

// narrow keeps the tables, definitions, sequences and table types named in
// objects. Native is opaque to the engine and copied through unchanged.
func narrow(s migrate.Schema, objects []migrate.Name) migrate.Schema {
	listed := func(n migrate.Name) bool { return slices.Contains(objects, n) }
	out := migrate.Schema{Native: s.Native}
	out.Tables = slices.DeleteFunc(slices.Clone(s.Tables), func(t migrate.Table) bool { return !listed(t.Name) })
	out.Definitions = slices.DeleteFunc(slices.Clone(s.Definitions), func(d migrate.Definition) bool { return !listed(d.Name) })
	out.Sequences = slices.DeleteFunc(slices.Clone(s.Sequences), func(q migrate.Sequence) bool { return !listed(q.Name) })
	out.TableTypes = slices.DeleteFunc(slices.Clone(s.TableTypes), func(t migrate.TableType) bool { return !listed(t.Name) })
	return out
}

// schemaObjects names every table, definition, sequence and table type of s.
func schemaObjects(s migrate.Schema) []migrate.Name {
	var names []migrate.Name
	for _, t := range s.Tables {
		names = append(names, t.Name)
	}
	for _, d := range s.Definitions {
		names = append(names, d.Name)
	}
	for _, q := range s.Sequences {
		names = append(names, q.Name)
	}
	for _, t := range s.TableTypes {
		names = append(names, t.Name)
	}
	return names
}

func canonicalNames(names []migrate.Name) []migrate.Name {
	out := slices.Clone(names)
	slices.SortFunc(out, compareName)
	return slices.Compact(out)
}

func canonicalRefusals(refusals []Refusal) []Refusal {
	out := slices.Clone(refusals)
	slices.SortFunc(out, func(a, b Refusal) int {
		return cmp.Or(compareName(a.Object, b.Object), cmp.Compare(a.Member, b.Member), cmp.Compare(a.Reason, b.Reason))
	})
	return slices.Compact(out)
}

func canonicalScope(s Scope) (Scope, error) {
	s, err := clone(s)
	if err != nil {
		return s, err
	}
	switch s.Mode {
	case ScopeDeclared, ScopeComplete:
	default:
		return s, fmt.Errorf("engine: scope mode must be %q or %q, got %q", ScopeDeclared, ScopeComplete, s.Mode)
	}
	slices.Sort(s.Schemas)
	slices.SortFunc(s.Objects, compareName)
	return s, nil
}

func canonicalDependents(deps []Dependent) ([]Dependent, error) {
	deps, err := clone(deps)
	if err != nil {
		return nil, err
	}
	for i := range deps {
		slices.SortFunc(deps[i].On, compareColumnRef)
		deps[i].On = slices.CompactFunc(deps[i].On, func(a, b ColumnRef) bool { return compareColumnRef(a, b) == 0 })
	}
	slices.SortStableFunc(deps, compareDependent)
	return deps, nil
}

func sortedUnique(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}
