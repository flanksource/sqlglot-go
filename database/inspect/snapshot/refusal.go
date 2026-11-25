package snapshot

import (
	"cmp"
	"errors"
	"slices"

	"github.com/flanksource/sqlglot-go/migrate"
)

// Refusal names an object, or one member of it, that the snapshot cannot model
// faithfully. An empty Member refuses the whole object, which the snapshot then
// leaves out; a member refusal leaves the object in and says which part of it a
// plan must not touch.
type Refusal struct {
	Object migrate.Name
	Member string
	Reason string
}

// collector gathers refusals when Options.CollectRefusals is set. A nil
// collector is strict: the first thing the snapshot cannot model fails the
// inspection with that reason, as it always has.
type collector struct {
	refusals []Refusal
	omitted  map[migrate.Name]bool
}

func newCollector() *collector { return &collector{omitted: map[migrate.Name]bool{}} }

// refuse records the refusal, or returns it as an error when strict.
func (r *collector) refuse(object migrate.Name, member, reason string) error {
	if r == nil {
		return errors.New(reason)
	}
	r.refusals = append(r.refusals, Refusal{Object: object, Member: member, Reason: reason})
	if member == "" {
		r.omitted[object] = true
	}
	return nil
}

// merged lists one refusal per object and member, with every reason found for it.
func (r *collector) merged() []Refusal {
	if r == nil {
		return nil
	}
	sorted := slices.Clone(r.refusals)
	slices.SortFunc(sorted, func(a, b Refusal) int {
		return cmp.Or(cmp.Compare(a.Object.Schema, b.Object.Schema), cmp.Compare(a.Object.Name, b.Object.Name), cmp.Compare(a.Member, b.Member), cmp.Compare(a.Reason, b.Reason))
	})
	var out []Refusal
	for _, f := range sorted {
		if n := len(out); n > 0 && out[n-1].Object == f.Object && out[n-1].Member == f.Member {
			if out[n-1].Reason != f.Reason {
				out[n-1].Reason += "; " + f.Reason
			}
			continue
		}
		out = append(out, f)
	}
	return out
}

// dropOmitted removes the objects a whole-object refusal named.
func (r *collector) dropOmitted(s migrate.Schema) migrate.Schema {
	if r == nil || len(r.omitted) == 0 {
		return s
	}
	s.Tables = slices.DeleteFunc(s.Tables, func(t migrate.Table) bool { return r.omitted[t.Name] })
	s.Definitions = slices.DeleteFunc(s.Definitions, func(d migrate.Definition) bool { return r.omitted[d.Name] })
	s.Sequences = slices.DeleteFunc(s.Sequences, func(q migrate.Sequence) bool { return r.omitted[q.Name] })
	s.TableTypes = slices.DeleteFunc(s.TableTypes, func(t migrate.TableType) bool { return r.omitted[t.Name] })
	return s
}
