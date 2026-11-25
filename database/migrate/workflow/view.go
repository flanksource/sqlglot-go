package workflow

import (
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/sqlglot-go/database/migrate/dialect"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// actionRebuild is the action of a change that drops and re-creates a
// dependent because another change touches its columns.
const actionRebuild = "rebuild"

// schemaResult is everything the CLI, the API and the web UI show of a schema
// plan, derived from its artifact alone.
func schemaResult(pf planFile) (PlanResult, error) {
	a := *pf.schema
	result := PlanResult{
		Dialect: a.Dialect, Schema: strings.Join(a.Scope.Schemas, ", "), Artifact: pf.body, Original: pf.original, Hash: a.Hash,
		Acks: engine.Acknowledgements(a), Applicable: len(pf.envelope.Advisory) == 0, Advisory: pf.envelope.Advisory,
		Warnings: slices.Concat(pf.envelope.Warnings, pf.envelope.Advisory), Sources: pf.envelope.Sources,
	}
	if err := result.addSchemaArtifact(a); err != nil {
		return PlanResult{}, err
	}
	return result, result.complete()
}

// addSchemaArtifact fills the plan's changes, steps and units from a schema
// artifact, whether it is the plan itself or the schema step of a script plan.
func (result *PlanResult) addSchemaArtifact(a engine.Artifact) error {
	changes, err := groupChanges(a.Changes)
	if err != nil {
		return err
	}
	result.Changes = changes
	result.Steps, result.Units = stepsAndUnits(a)
	result.Warnings = slices.Concat(result.Warnings, a.Warnings)
	result.Scope, result.Preserved, result.Refusals = &a.Scope, a.Preserved, a.Refusals
	return nil
}

// groupChanges lists the planned changes with each forced rebuild under the
// change that forced it.
func groupChanges(changes []migrate.Change) ([]Change, error) {
	grouped := []Change{}
	at := map[string]int{}
	for _, c := range changes {
		if c.Action != actionRebuild {
			at[c.ID] = len(grouped)
			grouped = append(grouped, Change{Change: c})
		}
	}
	for _, c := range changes {
		if c.Action != actionRebuild {
			continue
		}
		i, ok := at[c.Cause]
		if !ok {
			return nil, fmt.Errorf("rebuild %s names cause %q, which the artifact does not plan", c.ID, c.Cause)
		}
		grouped[i].Rebuilds = append(grouped[i].Rebuilds, c)
	}
	return grouped, nil
}

// stepsAndUnits flattens the units' ops to statements in execution order. A
// statement is destructive when the change it implements is.
func stepsAndUnits(a engine.Artifact) ([]Step, []Unit) {
	destructive := map[string]bool{}
	for _, c := range a.Changes {
		destructive[c.ID] = c.Destructive
	}
	steps, units := []Step{}, make([]Unit, len(a.Units))
	for i, u := range a.Units {
		units[i] = Unit{ID: u.ID, Changes: u.Changes, Objects: u.Objects, Transactional: u.Transactional, Preconditions: u.Preconditions, Steps: []string{}}
		for _, op := range u.Ops {
			for n, s := range op.Statements {
				id := fmt.Sprintf("%s/%d", op.ID, n+1)
				steps = append(steps, Step{ID: id, SQL: s.SQL, Destructive: destructive[op.Change], Change: op.Change, Unit: u.ID, Phase: string(op.Phase), NonTransactional: s.NonTransactional})
				units[i].Steps = append(units[i].Steps, id)
			}
		}
	}
	return steps, units
}

// complete renders the plan's statements as one script and gives the list
// fields their empty, non-null form.
func (result *PlanResult) complete() error {
	statements := make([]string, len(result.Steps))
	for i, step := range result.Steps {
		statements[i] = step.SQL
	}
	var err error
	if result.SQL, err = dialect.Script(result.Dialect, statements); err != nil {
		return err
	}
	if result.Acks == nil {
		result.Acks = []string{}
	}
	if result.Warnings == nil {
		result.Warnings = []string{}
	}
	if result.Changes == nil {
		result.Changes = []Change{}
	}
	return nil
}
