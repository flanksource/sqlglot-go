package sqlserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// artifactV1Version is the version of the v1 artifact BuildArtifact writes.
const artifactV1Version = 1

// Plan is the v1 offline planner: it plans the request with the engine and
// the pure Planner, so no dialect has to be registered, and flattens the
// ordered units into one step per statement.
func Plan(request migrate.Request) (migrate.Plan, error) {
	a, err := planV1(request, nil)
	if err != nil {
		return migrate.Plan{}, err
	}
	return planOfArtifact(a)
}

// BuildArtifact plans a v1 artifact whose snapshots are limited to
// input.Schemas and binds the plan to them with a hash.
func BuildArtifact(input migrate.ArtifactRequest) (migrate.Artifact, error) {
	if err := artifactScope(input.Schemas, input.Request); err != nil {
		return migrate.Artifact{}, err
	}
	a, err := planV1(input.Request, input.Schemas)
	if err != nil {
		return migrate.Artifact{}, err
	}
	plan, err := planOfArtifact(a)
	if err != nil {
		return migrate.Artifact{}, err
	}
	artifact := migrate.Artifact{
		Version: artifactV1Version, Schemas: a.Scope.Schemas, Plan: plan,
		Request: migrate.Request{Current: a.Baseline, Desired: a.Desired, Selection: a.Selection},
	}
	artifact.Hash, err = artifactV1Hash(artifact)
	return artifact, err
}

// ValidateArtifact checks the version and hash, then rebuilds the artifact
// from its own snapshots and requires the same hash, so edited SQL is refused.
func ValidateArtifact(artifact migrate.Artifact) error {
	if artifact.Version != artifactV1Version {
		return fmt.Errorf("sqlserver: unsupported migration artifact version %d, want %d", artifact.Version, artifactV1Version)
	}
	hash, err := artifactV1Hash(artifact)
	if err != nil {
		return err
	}
	if artifact.Hash != hash {
		return fmt.Errorf("sqlserver: migration artifact hash mismatch: recorded %s, content hashes to %s", artifact.Hash, hash)
	}
	expected, err := BuildArtifact(migrate.ArtifactRequest{Schemas: artifact.Schemas, Request: artifact.Request})
	if err != nil {
		return fmt.Errorf("sqlserver: rebuild migration artifact: %w", err)
	}
	if expected.Hash != artifact.Hash {
		return fmt.Errorf("sqlserver: migration artifact SQL does not match its snapshots and planner version: rebuild gives %s, recorded %s", expected.Hash, artifact.Hash)
	}
	return nil
}

func planV1(request migrate.Request, schemas []string) (engine.Artifact, error) {
	return engine.Plan(engine.PlanRequest{
		Dialect: Planner{}.Name(), Using: Planner{},
		Scope:   engine.Scope{Mode: engine.ScopeComplete, Schemas: schemas},
		Current: request.Current, Desired: request.Desired, Selection: request.Selection,
	})
}

// planOfArtifact lists every change the diff finds, as v1 did, with the
// rebuilds the plan forces, and one step per planned statement in order.
func planOfArtifact(a engine.Artifact) (migrate.Plan, error) {
	diff, err := Planner{}.Diff(a.Baseline, a.Desired)
	if err != nil {
		return migrate.Plan{}, err
	}
	plan := migrate.Plan{Steps: []migrate.Step{}, Changes: diff.Changes(), Warnings: a.Warnings}
	planned := map[string]migrate.Change{}
	for _, c := range a.Changes {
		planned[c.ID] = c
		if c.Cause != "" {
			plan.Changes = append(plan.Changes, c)
		}
	}
	for _, unit := range a.Units {
		for _, op := range unit.Ops {
			change, ok := planned[op.Change]
			if !ok {
				return migrate.Plan{}, fmt.Errorf("sqlserver: op %s implements unplanned change %s", op.ID, op.Change)
			}
			for _, s := range op.Statements {
				plan.Steps = append(plan.Steps, migrate.Step{Change: change, SQL: s.SQL})
			}
		}
	}
	return plan, nil
}

func artifactV1Hash(artifact migrate.Artifact) (string, error) {
	artifact.Hash = ""
	body, err := json.Marshal(artifact)
	if err != nil {
		return "", fmt.Errorf("sqlserver: hash migration artifact: %w", err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func artifactScope(schemas []string, request migrate.Request) error {
	if len(schemas) == 0 {
		return fmt.Errorf("sqlserver: migration artifact requires explicit schema scope")
	}
	seen := map[string]bool{}
	for _, name := range schemas {
		if name == "" || seen[name] {
			return fmt.Errorf("sqlserver: empty or duplicate artifact schema %q", name)
		}
		seen[name] = true
	}
	for _, snapshot := range []migrate.Schema{request.Current, request.Desired} {
		var names []migrate.Name
		for _, t := range snapshot.Tables {
			names = append(names, t.Name)
		}
		for _, d := range snapshot.Definitions {
			names = append(names, d.Name)
		}
		for _, q := range snapshot.Sequences {
			names = append(names, q.Name)
		}
		for _, t := range snapshot.TableTypes {
			names = append(names, t.Name)
		}
		for _, name := range names {
			if !seen[name.Schema] {
				return fmt.Errorf("sqlserver: %s.%s is outside artifact schema scope %v", name.Schema, name.Name, schemas)
			}
		}
	}
	return nil
}
