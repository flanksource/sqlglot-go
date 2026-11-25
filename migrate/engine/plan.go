package engine

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate"
)

const (
	actionDrop    = "drop"
	actionRebuild = "rebuild"
)

type PlanRequest struct {
	Dialect string
	Scope   Scope
	// Context lists objects present in Current only because a dependent sits on
	// them. They are not managed: no diff change on them is planned or preserved.
	Context []migrate.Name
	Current migrate.Schema
	Desired migrate.Schema
	// Selection limits the plan to those change IDs; empty plans every change.
	Selection []string
	// SelectNone plans no change at all (an artifact that records what it left
	// out and runs nothing). It excludes Selection.
	SelectNone   bool
	Dependents   []Dependent
	Refusals     []Refusal
	Capabilities Capabilities
	Atomic       bool
}

// Plan diffs Current against Desired and plans the selected changes, the
// rebuilds they force, their global order, preconditions and units. It reads
// no database: live dependents and refusals arrive in the request.
func Plan(req PlanRequest) (Artifact, error) {
	d, err := Lookup(req.Dialect)
	if err != nil {
		return Artifact{}, err
	}
	if req.Capabilities, err = planCapabilities(d, req.Capabilities); err != nil {
		return Artifact{}, err
	}
	req, err = canonicalRequest(req)
	if err != nil {
		return Artifact{}, err
	}
	diff, err := d.Diff(req.Current, req.Desired)
	if err != nil {
		return Artifact{}, fmt.Errorf("engine: %s diff: %w", req.Dialect, err)
	}
	res, err := resolveChanges(diff, req)
	if err != nil {
		return Artifact{}, err
	}
	rebuilds, err := forcedRebuilds(d, diff, res.selected, req)
	if err != nil {
		return Artifact{}, err
	}
	changes := slices.Concat(res.selected, rebuildChanges(rebuilds))
	untouched, err := checkRefusals(req.Refusals, changes)
	if err != nil {
		return Artifact{}, err
	}
	ops, err := lowerOrdered(diff, changes, rebuilds, ids(res.selected))
	if err != nil {
		return Artifact{}, err
	}
	pre, err := preconditions(d, changes, req)
	if err != nil {
		return Artifact{}, err
	}
	a := Artifact{
		Version: ArtifactVersion, Dialect: req.Dialect, Capabilities: req.Capabilities, Scope: req.Scope, Context: req.Context,
		Baseline: req.Current, Desired: req.Desired, Selection: req.Selection, SelectNone: req.SelectNone, Dependents: rebuiltDependents(rebuilds),
		Refusals: untouched, Warnings: sortedUnique(diff.Warnings()),
		Changes: sortedChanges(changes), Preserved: sortedChanges(res.preserved), Atomic: req.Atomic,
		Units: buildUnits(ops, changes, pre, req.Capabilities.TransactionalDDL, req.Atomic),
	}
	a.Hash, err = artifactHash(a)
	return a, err
}

// planCapabilities keeps what the caller read from a database, and otherwise
// (the zero value, an offline plan) takes the dialect's static capabilities,
// which carry no fingerprint: the artifact is not bound to one server.
func planCapabilities(d Dialect, given Capabilities) (Capabilities, error) {
	if given.Fingerprint != "" || given.TransactionalDDL || len(given.Features) > 0 {
		return given, nil
	}
	offline, err := d.Capabilities(context.Background(), nil)
	if err != nil {
		return Capabilities{}, fmt.Errorf("engine: %s offline capabilities: %w", d.Name(), err)
	}
	if offline.Fingerprint != "" {
		return Capabilities{}, fmt.Errorf("engine: %s offline capabilities carry fingerprint %q, an offline plan has none", d.Name(), offline.Fingerprint)
	}
	return offline, nil
}

func canonicalRequest(req PlanRequest) (PlanRequest, error) {
	var err error
	if req.Scope, err = canonicalScope(req.Scope); err != nil {
		return req, err
	}
	if req.Current, err = canonicalSchema(req.Current); err != nil {
		return req, err
	}
	if req.Desired, err = canonicalSchema(req.Desired); err != nil {
		return req, err
	}
	if req.Dependents, err = canonicalDependents(req.Dependents); err != nil {
		return req, err
	}
	if req.Capabilities, err = clone(req.Capabilities); err != nil {
		return req, err
	}
	req.Context = canonicalNames(req.Context)
	req.Refusals = canonicalRefusals(req.Refusals)
	req.Selection = sortedUnique(req.Selection)
	if req.SelectNone && len(req.Selection) > 0 {
		return req, fmt.Errorf("engine: SelectNone and a selection of %d changes exclude each other", len(req.Selection))
	}
	if req.Scope.Mode == ScopeDeclared {
		if len(req.Scope.Objects) == 0 {
			return req, fmt.Errorf("engine: a declared scope must name the objects it manages in Scope.Objects (PlanLive defaults them to the desired schema's)")
		}
		req.Desired = narrow(req.Desired, req.Scope.Objects)
	}
	return req, checkPartialTables(req)
}

type resolved struct {
	selected  []migrate.Change // diff order
	preserved []migrate.Change
	// preservedWhy says, by change ID, which rule left each preserved drop unplanned.
	preservedWhy map[string]string
	context      []migrate.Change
}

// excluded names why each change the plan leaves out cannot be selected or required.
func (r resolved) excluded() map[string]string {
	out := map[string]string{}
	for _, c := range r.preserved {
		out[c.ID] = r.preservedWhy[c.ID]
	}
	for _, c := range r.context {
		out[c.ID] = "a change on context object " + nameLabel(c.Object)
	}
	return out
}

// resolveChanges leaves out the changes on context objects, applies the drop
// policy, then the selection, and fails closed when a selected change needs one
// that is not selected.
func resolveChanges(diff Diff, req PlanRequest) (resolved, error) {
	res := resolved{preservedWhy: map[string]string{}}
	var planned []migrate.Change
	seen := map[string]bool{}
	for _, c := range diff.Changes() {
		if seen[c.ID] {
			return res, fmt.Errorf("engine: diff lists change %s twice", c.ID)
		}
		seen[c.ID] = true
		c.Requires = sortedUnique(c.Requires)
		if slices.Contains(req.Context, c.Object) {
			res.context = append(res.context, c)
		} else if why := preservedBecause(c, req); why != "" {
			res.preserved = append(res.preserved, c)
			res.preservedWhy[c.ID] = why
		} else {
			planned = append(planned, c)
		}
	}
	if req.SelectNone {
		return res, nil
	}
	if len(req.Selection) == 0 {
		res.selected = planned
	} else if err := selectChanges(&res, planned, req.Selection); err != nil {
		return res, err
	}
	return res, checkRequires(res)
}

func selectChanges(res *resolved, planned []migrate.Change, selection []string) error {
	chosen := map[string]bool{}
	for _, id := range selection {
		chosen[id] = true
	}
	for _, c := range planned {
		if chosen[c.ID] {
			res.selected = append(res.selected, c)
			delete(chosen, c.ID)
		}
	}
	for id, why := range res.excluded() {
		if chosen[id] {
			return fmt.Errorf("engine: selected change %s is %s", id, why)
		}
	}
	if len(chosen) > 0 {
		unknown := slices.Sorted(func(yield func(string) bool) {
			for id := range chosen {
				if !yield(id) {
					return
				}
			}
		})
		return fmt.Errorf("engine: selected changes are not in the diff: %s", strings.Join(unknown, ", "))
	}
	return nil
}

func checkRequires(res resolved) error {
	selected := map[string]bool{}
	for _, c := range res.selected {
		selected[c.ID] = true
	}
	excluded := res.excluded()
	for _, c := range res.selected {
		var missing []string
		for _, r := range c.Requires {
			if selected[r] {
				continue
			}
			if why, ok := excluded[r]; ok {
				return fmt.Errorf("engine: change %s requires %s, %s", c.ID, r, why)
			}
			missing = append(missing, r)
		}
		if len(missing) > 0 {
			return fmt.Errorf("engine: selected change %s requires unselected changes %s", c.ID, strings.Join(missing, ", "))
		}
	}
	return nil
}

func ids(changes []migrate.Change) []string {
	out := make([]string, len(changes))
	for i, c := range changes {
		out[i] = c.ID
	}
	return out
}

func sortedChanges(changes []migrate.Change) []migrate.Change {
	out := slices.Concat([]migrate.Change{}, changes)
	slices.SortFunc(out, func(a, b migrate.Change) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// checkRefusals fails when a planned change or rebuild touches an object or
// member inspection refused, and returns the refusals nothing touches.
func checkRefusals(refusals []Refusal, changes []migrate.Change) ([]Refusal, error) {
	var untouched []Refusal
	for _, r := range refusals {
		i := slices.IndexFunc(changes, func(c migrate.Change) bool {
			return c.Object == r.Object && (r.Member == "" || c.Member == r.Member)
		})
		if i >= 0 {
			return nil, fmt.Errorf("engine: change %s touches %s, which inspection refused: %s", changes[i].ID, refusalLabel(r), r.Reason)
		}
		untouched = append(untouched, r)
	}
	return untouched, nil
}

func refusalLabel(r Refusal) string {
	if r.Member == "" {
		return nameLabel(r.Object)
	}
	return nameLabel(r.Object) + "." + r.Member
}

// rebuiltDependents are the dependents the plan re-creates, which is what a
// unit's postcondition verifies; the artifact keeps them whether the caller
// supplied them or the dialect derived them offline.
func rebuiltDependents(rebuilds []Rebuild) []Dependent {
	out := make([]Dependent, len(rebuilds))
	for i, r := range rebuilds {
		out[i] = r.Dependent
	}
	slices.SortStableFunc(out, compareDependent)
	return out
}

// preconditions asks the dialect for each planned change's guards and checks
// they are addressable: unique IDs, owned by the change they were asked for.
func preconditions(d Dialect, changes []migrate.Change, req PlanRequest) (map[string][]Precondition, error) {
	out := map[string][]Precondition{}
	seen := map[string]bool{}
	for _, c := range changes {
		list, err := d.Preconditions(c, req.Current, req.Desired, req.Capabilities)
		if err != nil {
			return nil, fmt.Errorf("engine: preconditions for %s: %w", c.ID, err)
		}
		for _, p := range list {
			if p.ID == "" || p.SQL == "" {
				return nil, fmt.Errorf("engine: precondition %q of change %s needs an ID and SQL", p.ID, c.ID)
			}
			if p.Change != c.ID {
				return nil, fmt.Errorf("engine: precondition %s names change %q, planned for %s", p.ID, p.Change, c.ID)
			}
			if seen[p.ID] {
				return nil, fmt.Errorf("engine: duplicate precondition ID %s", p.ID)
			}
			seen[p.ID] = true
			out[c.ID] = append(out[c.ID], p)
		}
	}
	return out, nil
}
