// Package sqlserver plans SQL Server schema migrations without a database connection.
package sqlserver

import (
	"context"
	"fmt"
	"slices"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
	atlas "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
)

// Plan validates both snapshots and returns ordered SQL and typed change metadata.
// Names are exact catalog identifiers; callers must supply complete foreign-key targets.
func Plan(request migrate.Request) (migrate.Plan, error) {
	from, to, err := realms(request)
	if err != nil {
		return migrate.Plan{}, err
	}
	changes, err := atlas.DefaultDiff.RealmDiff(from, to)
	if err != nil {
		return migrate.Plan{}, err
	}
	metadata := make(map[schema.Change]migrate.Change)
	result := migrate.Plan{Steps: []migrate.Step{}, Changes: []migrate.Change{}}
	for _, c := range changes {
		if err := describe(c, nil, metadata, &result.Changes); err != nil {
			return migrate.Plan{}, err
		}
	}
	plan, err := atlas.DefaultPlan.PlanChanges(context.Background(), "migration", changes)
	if err != nil {
		return migrate.Plan{}, err
	}
	for _, step := range plan.Changes {
		change, ok := metadata[step.Source]
		if !ok {
			if err := describe(step.Source, nil, metadata, &result.Changes); err != nil {
				return migrate.Plan{}, err
			}
			change = metadata[step.Source]
		}
		result.Steps = append(result.Steps, migrate.Step{Change: change, SQL: step.Cmd})
	}
	runtime := map[migrate.Name][]migrate.RuntimeDependency{}
	for _, d := range request.Desired.Definitions {
		runtime[d.Name] = d.RuntimeDependencies
	}
	for i := range result.Changes {
		flagRuntimeDependencies(&result.Changes[i], runtime)
	}
	for i := range result.Steps {
		flagRuntimeDependencies(&result.Steps[i].Change, runtime)
	}
	return result, nil
}

func flagRuntimeDependencies(change *migrate.Change, runtime map[migrate.Name][]migrate.RuntimeDependency) {
	if change.Action != "drop" && slices.Contains([]string{"procedure", "function", "view", "trigger"}, change.Kind) {
		change.RuntimeDependencies = runtime[change.Object]
	}
}

func realms(request migrate.Request) (*schema.Realm, *schema.Realm, error) {
	var namespaces []string
	for _, snapshot := range []migrate.Schema{request.Current, request.Desired} {
		for _, t := range snapshot.Tables {
			namespaces = append(namespaces, t.Name.Schema)
		}
		for _, d := range snapshot.Definitions {
			namespaces = append(namespaces, d.Name.Schema)
		}
		for _, q := range snapshot.Sequences {
			namespaces = append(namespaces, q.Name.Schema)
		}
		for _, t := range snapshot.TableTypes {
			namespaces = append(namespaces, t.Name.Schema)
		}
	}
	slices.Sort(namespaces)
	namespaces = slices.Compact(namespaces)
	if slices.Contains(namespaces, "") {
		return nil, nil, fmt.Errorf("sqlserver: all objects require an explicit schema")
	}
	from, err := realm(request.Current, namespaces)
	if err != nil {
		return nil, nil, fmt.Errorf("current snapshot: %w", err)
	}
	to, err := realm(request.Desired, namespaces)
	if err != nil {
		return nil, nil, fmt.Errorf("desired snapshot: %w", err)
	}
	return from, to, nil
}
