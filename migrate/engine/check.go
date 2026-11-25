package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate"
)

type UnitState string

const (
	UnitApplied UnitState = "applied"
	UnitPending UnitState = "pending"
)

// DriftError reports a unit whose objects are neither at the artifact's
// baseline nor at its desired state.
type DriftError struct {
	Unit    string
	Changes []migrate.Change
}

func (e *DriftError) Error() string {
	return fmt.Sprintf("engine: unit %s has drifted from the artifact baseline: %d catalog changes since preview", e.Unit, len(e.Changes))
}

// PartialError reports a unit whose own changes are in place but a dependent
// it dropped to make them is missing or differs: it is neither applied nor
// pending, and re-running it would not bring the dependent back.
type PartialError struct {
	Unit    string
	Missing []Dependent
}

func (e *PartialError) Error() string {
	lines := make([]string, len(e.Missing))
	for i, d := range e.Missing {
		lines[i] = fmt.Sprintf("%s %s dropped by this unit and not re-created", d.Kind, dependentLabel(d))
	}
	return fmt.Sprintf("engine: unit %s is partially applied: %s", e.Unit, strings.Join(lines, "; "))
}

// Check classifies every unit of a validated artifact against the database as
// applied or pending. A unit that is in neither state is a *DriftError or a
// *PartialError.
func Check(ctx context.Context, q Querier, a Artifact) (map[string]UnitState, error) {
	if err := Validate(a); err != nil {
		return nil, err
	}
	d, err := Lookup(a.Dialect)
	if err != nil {
		return nil, err
	}
	states := make(map[string]UnitState, len(a.Units))
	for _, u := range a.Units {
		if states[u.ID], err = classifyUnit(ctx, d, q, a, u); err != nil {
			return nil, err
		}
	}
	return states, nil
}

// classifyUnit inspects only the unit's objects and diffs both sides narrowed
// to them. The unit is applied when the diff to desired no longer holds any of
// its own changes and every dependent it rebuilt is back, pending when its
// objects still equal the baseline, and drifted otherwise.
func classifyUnit(ctx context.Context, d Dialect, q Querier, a Artifact, u Unit) (UnitState, error) {
	own := ownChanges(a, u)
	if len(own) == 0 {
		return "", fmt.Errorf("engine: unit %s has no planned change of its own", u.ID)
	}
	insp, err := d.Inspect(ctx, q, Scope{Mode: ScopeDeclared, Schemas: a.Scope.Schemas, Objects: u.Objects})
	if err != nil {
		return "", fmt.Errorf("engine: inspect unit %s: %w", u.ID, err)
	}
	actual := narrow(insp.Schema, u.Objects)
	desired, err := d.Reconcile(ctx, q, actual, narrow(a.Desired, u.Objects))
	if err != nil {
		return "", fmt.Errorf("engine: reconcile unit %s: %w", u.ID, err)
	}
	toDesired, err := d.Diff(actual, desired)
	if err != nil {
		return "", fmt.Errorf("engine: diff unit %s to desired: %w", u.ID, err)
	}
	if !slices.ContainsFunc(toDesired.Changes(), func(c migrate.Change) bool { return own[c.ID] }) {
		if err := verifyRebuilt(ctx, d, q, a, u, actual); err != nil {
			return "", err
		}
		return UnitApplied, nil
	}
	toBaseline, err := d.Diff(actual, narrow(a.Baseline, u.Objects))
	if err != nil {
		return "", fmt.Errorf("engine: diff unit %s to baseline: %w", u.ID, err)
	}
	if drifted := toBaseline.Changes(); len(drifted) > 0 {
		return "", &DriftError{Unit: u.ID, Changes: drifted}
	}
	return UnitPending, nil
}

// ownChanges are the unit's planned changes other than forced rebuilds, which
// no diff of the database lists.
func ownChanges(a Artifact, u Unit) map[string]bool {
	own := map[string]bool{}
	for _, id := range u.Changes {
		if !slices.ContainsFunc(a.Changes, func(c migrate.Change) bool { return c.ID == id && c.Action == actionRebuild }) {
			own[id] = true
		}
	}
	return own
}

// verifyRebuilt requires every dependent the unit rebuilt to exist again with
// the definition the artifact recorded; a diff of declared objects cannot see
// an undeclared index or foreign key that was dropped and never re-created.
func verifyRebuilt(ctx context.Context, d Dialect, q Querier, a Artifact, u Unit, actual migrate.Schema) error {
	rebuilt, err := rebuiltIn(a, u)
	if err != nil || len(rebuilt) == 0 {
		return err
	}
	var columns []ColumnRef
	for _, dep := range rebuilt {
		columns = append(columns, dep.On...)
	}
	slices.SortFunc(columns, compareColumnRef)
	live, err := d.Dependents(ctx, q, actual, slices.Compact(columns))
	if err != nil {
		return fmt.Errorf("engine: read dependents of unit %s: %w", u.ID, err)
	}
	var missing []Dependent
	for _, dep := range rebuilt {
		i := slices.IndexFunc(live, func(l Dependent) bool { return compareDependent(l, dep) == 0 })
		if i < 0 {
			missing = append(missing, dep)
			continue
		}
		same, err := d.SameDependent(dep, live[i])
		if err != nil {
			return err
		}
		if !same {
			missing = append(missing, dep)
		}
	}
	if len(missing) > 0 {
		return &PartialError{Unit: u.ID, Missing: missing}
	}
	return nil
}

// rebuiltIn matches the unit's rebuild changes to the artifact's dependents.
func rebuiltIn(a Artifact, u Unit) ([]Dependent, error) {
	var out []Dependent
	for _, c := range a.Changes {
		if c.Action != actionRebuild || !slices.Contains(u.Changes, c.ID) {
			continue
		}
		probe := Dependent{Kind: DependentKind(c.Kind), Table: c.Object, Name: c.Member}
		i := slices.IndexFunc(a.Dependents, func(dep Dependent) bool { return compareDependent(dep, probe) == 0 })
		if i < 0 {
			return nil, fmt.Errorf("engine: rebuild %s of unit %s has no recorded dependent", c.ID, u.ID)
		}
		out = append(out, a.Dependents[i])
	}
	return out, nil
}

// SameDefinition compares what two dependents re-create by canonical JSON; On
// and Refusal describe why one was found, not what it is. Dialects delegate to
// it from SameDependent when they have no looser rule.
func SameDefinition(a, b Dependent) (bool, error) {
	type definition struct {
		Index      *migrate.Index
		ForeignKey *migrate.ForeignKey
		Check      *migrate.Check
		Definition string
	}
	left, err := json.Marshal(definition{a.Index, a.ForeignKey, a.Check, a.Definition})
	if err != nil {
		return false, fmt.Errorf("engine: compare dependent %s: %w", dependentLabel(a), err)
	}
	right, err := json.Marshal(definition{b.Index, b.ForeignKey, b.Check, b.Definition})
	if err != nil {
		return false, fmt.Errorf("engine: compare dependent %s: %w", dependentLabel(b), err)
	}
	return string(left) == string(right), nil
}
