package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	bundle "github.com/flanksource/sqlglot-go/database/migrate"
	"github.com/flanksource/sqlglot-go/database/migrate/review"
	executor "github.com/flanksource/sqlglot-go/database/migrate/sqlserver"
	native "github.com/flanksource/sqlglot-go/migrate"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
)

func Unwrap(data []byte) (json.RawMessage, []string, error) {
	var envelope struct {
		Artifact          json.RawMessage `json:"artifact"`
		SQLServerArtifact json.RawMessage `json:"sqlServerArtifact"`
		Warnings          []string        `json:"warnings"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, nil, err
	}
	if len(envelope.Artifact) > 0 && len(envelope.SQLServerArtifact) > 0 {
		return nil, nil, fmt.Errorf("artifact envelope contains two artifacts")
	}
	if len(envelope.Artifact) > 0 {
		return envelope.Artifact, envelope.Warnings, nil
	}
	if len(envelope.SQLServerArtifact) > 0 {
		return envelope.SQLServerArtifact, envelope.Warnings, nil
	}
	return json.RawMessage(data), envelope.Warnings, nil
}

func Review(data []byte, dialect, schema string) (PlanResult, error) {
	var preview bundle.Preview
	if err := json.Unmarshal(data, &preview); err != nil {
		return PlanResult{}, err
	}
	if preview.SQLServerPlan != nil {
		if preview.Artifact != nil || preview.SQLServerArtifact != nil || len(preview.Warnings) == 0 || (dialect != "" && dialect != "sqlserver") {
			return PlanResult{}, fmt.Errorf("invalid advisory SQL Server preview")
		}
		result := PlanResult{Dialect: "sqlserver", Schema: schema, Artifact: data, Original: data, Warnings: preview.Warnings, Steps: []Step{}, Acks: []string{}, Changes: preview.SQLServerPlan.Changes}
		for _, step := range preview.SQLServerPlan.Steps {
			result.Steps = append(result.Steps, Step{ID: step.Change.ID, SQL: step.SQL, Destructive: step.Change.Destructive})
		}
		result.complete()
		return result, nil
	}
	artifact, warnings, err := Unwrap(data)
	if err != nil {
		return PlanResult{}, err
	}
	var scope struct {
		Version  int             `json:"version"`
		Schemas  []string        `json:"schemas"`
		Baseline review.Snapshot `json:"baseline"`
	}
	if err := json.Unmarshal(artifact, &scope); err != nil {
		return PlanResult{}, err
	}
	if dialect == "" {
		if scope.Version == 1 {
			dialect = "sqlserver"
		} else {
			dialect = scope.Baseline.Dialect
		}
	}
	if schema == "" {
		if dialect == "sqlserver" {
			schema = strings.Join(scope.Schemas, ", ")
		} else {
			schema = scope.Baseline.Schema
		}
	}
	result := PlanResult{Dialect: dialect, Schema: schema, Original: data, Artifact: artifact, Steps: []Step{}, Acks: []string{}, Warnings: warnings}
	if dialect == "sqlserver" {
		var a native.Artifact
		if err = Decode(artifact, &a); err != nil {
			return result, err
		}
		result.Hash, result.Acks, result.Changes = a.Hash, planner.Acknowledgements(a), a.Plan.Changes
		for _, step := range a.Plan.Steps {
			result.Steps = append(result.Steps, Step{ID: step.Change.ID, SQL: step.SQL, Destructive: step.Change.Destructive})
		}
		if err = executor.Validate(executor.Request{Artifact: a, Hash: a.Hash, Acknowledgements: result.Acks}); err != nil {
			return result, err
		}
		result.Applicable = true
	} else {
		var a review.Artifact
		if err = Decode(artifact, &a); err != nil {
			return result, err
		}
		if a.Baseline.Dialect != dialect || a.Baseline.Schema != schema {
			return result, fmt.Errorf("artifact dialect or schema does not match target")
		}
		result.Hash, result.Acks, result.Changes = a.Hash, a.Acknowledgements(), a.Changes
		result.Warnings = append(result.Warnings, a.Advisory...)
		for _, step := range a.Steps {
			result.Steps = append(result.Steps, Step{ID: step.ID, SQL: step.SQL, Destructive: step.Destructive})
		}
		result.Applicable = len(a.Advisory) == 0
		if result.Applicable {
			if err = review.Validate(review.Request{Artifact: a, Hash: a.Hash, Acknowledgements: result.Acks}); err != nil {
				return result, err
			}
		}
	}
	result.complete()
	return result, nil
}

func (result *PlanResult) complete() {
	var sql strings.Builder
	for _, step := range result.Steps {
		sql.WriteString(strings.TrimSuffix(strings.TrimSpace(step.SQL), ";"))
		if result.Dialect == "sqlserver" {
			sql.WriteString("\nGO\n")
		} else {
			sql.WriteString(";\n")
		}
	}
	result.SQL = sql.String()
	if result.Acks == nil {
		result.Acks = []string{}
	}
	if result.Warnings == nil {
		result.Warnings = []string{}
	}
	if result.Changes == nil {
		result.Changes = []native.Change{}
	}
}

func Validate(opts ApplyOptions) error {
	if opts.Hash == "" {
		return fmt.Errorf("reviewed hash is required")
	}
	data, _, err := Unwrap(opts.Artifact)
	if err != nil {
		return err
	}
	var envelope struct {
		Version int `json:"version"`
	}
	if err = json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	if envelope.Version == 1 {
		var artifact native.Artifact
		if err = Decode(data, &artifact); err != nil {
			return err
		}
		return executor.Validate(executor.Request{Artifact: artifact, Hash: opts.Hash, Acknowledgements: opts.Acks, LockTimeout: opts.LockTimeout})
	}
	var artifact review.Artifact
	if err = Decode(data, &artifact); err != nil {
		return err
	}
	return review.Validate(review.Request{Artifact: artifact, Hash: opts.Hash, Acknowledgements: opts.Acks, LockTimeout: opts.LockTimeout})
}

func Apply(ctx context.Context, db *sql.DB, opts ApplyOptions) (any, error) {
	if err := Validate(opts); err != nil {
		return nil, err
	}
	data, _, err := Unwrap(opts.Artifact)
	if err != nil {
		return nil, err
	}
	if opts.Dialect == "sqlserver" {
		var artifact native.Artifact
		if err = Decode(data, &artifact); err != nil {
			return nil, err
		}
		return executor.Apply(ctx, db, executor.Request{Artifact: artifact, Hash: opts.Hash, Acknowledgements: opts.Acks, LockTimeout: opts.LockTimeout})
	}
	var artifact review.Artifact
	if err = Decode(data, &artifact); err != nil {
		return nil, err
	}
	if artifact.Baseline.Dialect != opts.Dialect || artifact.Baseline.Schema != opts.Schema {
		return nil, fmt.Errorf("artifact dialect or schema does not match target")
	}
	return review.Apply(ctx, db, review.Request{Artifact: artifact, Hash: opts.Hash, Acknowledgements: opts.Acks, LockTimeout: opts.LockTimeout})
}
