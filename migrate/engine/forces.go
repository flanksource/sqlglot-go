package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate"
)

// forcedRebuilds finds the dependents of every selected forcing change that
// the diff does not already change, and returns a rebuild for each.
func forcedRebuilds(d Dialect, diff Diff, selected []migrate.Change, req PlanRequest) ([]Rebuild, error) {
	forcings, err := selectedForcings(diff, selected)
	if err != nil || len(forcings) == 0 {
		return nil, err
	}
	deps, err := forcingDependents(d, req, forcings)
	if err != nil {
		return nil, err
	}
	retyped := retypedColumns(forcings)
	var rebuilds []Rebuild
	for _, dep := range deps {
		hits := hitting(forcings, dep)
		if len(hits) == 0 {
			continue
		}
		existing, found := targeting(selected, dep)
		if !found || existing.Action != actionDrop {
			if err := checkPartners(dep, hits, retyped); err != nil {
				return nil, err
			}
		}
		if !found {
			if err := checkDroppedColumns(dep, hits); err != nil {
				return nil, err
			}
		}
		if found {
			continue
		}
		if dep.Refusal != "" {
			return nil, fmt.Errorf("engine: cannot rebuild %s %s (forced by %s): %s", dep.Kind, dependentLabel(dep), hits[0].Change, dep.Refusal)
		}
		rebuild, err := newRebuild(dep, hits)
		if err != nil {
			return nil, err
		}
		rebuilds = append(rebuilds, rebuild)
	}
	slices.SortFunc(rebuilds, func(a, b Rebuild) int { return strings.Compare(a.Change.ID, b.Change.ID) })
	return rebuilds, nil
}

func rebuildChanges(rebuilds []Rebuild) []migrate.Change {
	out := make([]migrate.Change, len(rebuilds))
	for i, r := range rebuilds {
		out[i] = r.Change
	}
	return out
}

// selectedForcings keeps the diff's forcings whose change is selected. A
// forcing that names a change the diff does not list is a dialect defect.
func selectedForcings(diff Diff, selected []migrate.Change) ([]Forcing, error) {
	inDiff := map[string]bool{}
	for _, c := range diff.Changes() {
		inDiff[c.ID] = true
	}
	chosen := map[string]bool{}
	for _, c := range selected {
		chosen[c.ID] = true
	}
	var out []Forcing
	for _, f := range diff.Forcing() {
		if !inDiff[f.Change] {
			return nil, fmt.Errorf("engine: forcing names change %s, which the diff does not list", f.Change)
		}
		if chosen[f.Change] {
			out = append(out, f)
		}
	}
	return out, nil
}

// forcingDependents merges the dependents the caller supplied with those the
// dialect derives offline from the current schema.
func forcingDependents(d Dialect, req PlanRequest, forcings []Forcing) ([]Dependent, error) {
	derived, err := d.Dependents(context.Background(), nil, req.Current, forcingColumns(forcings))
	if err != nil {
		return nil, fmt.Errorf("engine: derive dependents: %w", err)
	}
	derived, err = canonicalDependents(derived)
	if err != nil {
		return nil, err
	}
	return mergeDependents(req.Dependents, derived), nil
}

// mergeDependents de-duplicates by kind, table and member, keeping the first
// definition and the union of the columns each depends on.
func mergeDependents(lists ...[]Dependent) []Dependent {
	var out []Dependent
	for _, list := range lists {
		for _, dep := range list {
			i := slices.IndexFunc(out, func(o Dependent) bool { return compareDependent(o, dep) == 0 })
			if i < 0 {
				out = append(out, dep)
				continue
			}
			out[i].On = append(out[i].On, dep.On...)
			slices.SortFunc(out[i].On, compareColumnRef)
			out[i].On = slices.Compact(out[i].On)
		}
	}
	slices.SortStableFunc(out, compareDependent)
	return out
}

func hitting(forcings []Forcing, dep Dependent) []Forcing {
	var hits []Forcing
	for _, f := range forcings {
		if slices.ContainsFunc(f.Columns, func(c ColumnRef) bool { return slices.Contains(dep.On, c) }) {
			hits = append(hits, f)
		}
	}
	return hits
}

// targeting finds a selected change that already acts on the dependent.
func targeting(selected []migrate.Change, dep Dependent) (migrate.Change, bool) {
	i := slices.IndexFunc(selected, func(c migrate.Change) bool {
		return c.Kind == string(dep.Kind) && c.Object == dep.Table && c.Member == memberOf(dep)
	})
	if i < 0 {
		return migrate.Change{}, false
	}
	return selected[i], true
}

func retypedColumns(forcings []Forcing) map[ColumnRef]bool {
	out := map[ColumnRef]bool{}
	for _, f := range forcings {
		if !f.Retype {
			continue
		}
		for _, c := range f.Columns {
			out[c] = true
		}
	}
	return out
}

// checkPartners enforces the foreign-key partner rule: a foreign key pairs a
// retyped column with one on the other side, and that column must be retyped in
// the same plan or the keys' types would diverge.
func checkPartners(dep Dependent, hits []Forcing, retyped map[ColumnRef]bool) error {
	if dep.Kind != DependentForeignKey {
		return nil
	}
	fk := dep.ForeignKey
	if fk == nil || len(fk.Columns) != len(fk.ReferencedColumns) {
		return fmt.Errorf("engine: foreign key %s needs matching columns and referencedColumns", dependentLabel(dep))
	}
	for _, f := range hits {
		if !f.Retype {
			continue
		}
		for i := range fk.Columns {
			child := ColumnRef{Table: dep.Table, Column: fk.Columns[i]}
			parent := ColumnRef{Table: fk.References, Column: fk.ReferencedColumns[i]}
			for _, partner := range partnersOf(f.Columns, child, parent) {
				if !retyped[partner] {
					return fmt.Errorf("engine: foreign key %s pairs a column changed by %s with %s.%s, which must be changed in the same plan",
						dependentLabel(dep), f.Change, nameLabel(partner.Table), partner.Column)
				}
			}
		}
	}
	return nil
}

// checkDroppedColumns refuses a dependent on a column the plan drops that no
// selected change drops with it: it would have to be re-created over a column
// that is gone, so the plan fails before anything runs and names it. A
// dependent a selected change already acts on (an index or foreign key the plan
// drops, on this table or on another one declared in the same plan) is never
// asked about here.
func checkDroppedColumns(dep Dependent, hits []Forcing) error {
	for _, f := range hits {
		i := slices.IndexFunc(f.Columns, func(c ColumnRef) bool { return c.Dropped != "" && slices.Contains(dep.On, c) })
		if i < 0 {
			continue
		}
		dropped := f.Columns[i]
		what := nameLabel(dropped.Table) + "." + dropped.Column
		if dep.Refusal != "" {
			return fmt.Errorf("engine: cannot rebuild %s %s (forced by %s): %s", dep.Kind, dependentLabel(dep), f.Change, dep.Refusal)
		}
		return fmt.Errorf("engine: %s %s depends on column %s, which %s drops, so it cannot be re-created afterwards: drop it in the same plan or declare it away",
			dep.Kind, dependentLabel(dep), what, f.Change)
	}
	return nil
}

func partnersOf(columns []ColumnRef, child, parent ColumnRef) []ColumnRef {
	var out []ColumnRef
	if slices.Contains(columns, child) {
		out = append(out, parent)
	}
	if slices.Contains(columns, parent) {
		out = append(out, child)
	}
	return out
}

func newRebuild(dep Dependent, hits []Forcing) (Rebuild, error) {
	causes := make([]string, len(hits))
	for i, f := range hits {
		causes[i] = f.Change
	}
	causes = sortedUnique(causes)
	member := memberOf(dep)
	id, err := json.Marshal([]string{actionRebuild, string(dep.Kind), dep.Table.Schema, dep.Table.Name, member})
	if err != nil {
		return Rebuild{}, err
	}
	return Rebuild{Dependent: dep, Change: migrate.Change{
		ID: string(id), Action: actionRebuild, Kind: string(dep.Kind), Object: dep.Table, Member: member,
		Reason:   fmt.Sprintf("%s %s is dropped and re-created because %s changes the columns it depends on", dep.Kind, dependentLabel(dep), causes[0]),
		Requires: causes, Cause: causes[0],
	}}, nil
}

func nameLabel(n migrate.Name) string { return n.Schema + "." + n.Name }

func dependentLabel(dep Dependent) string {
	if member := memberOf(dep); member != "" {
		return nameLabel(dep.Table) + "." + member
	}
	return nameLabel(dep.Table)
}
