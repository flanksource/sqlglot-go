package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/flanksource/sqlglot-go/database/migrate/csvdata"
)

func resolveCSV(opts Options) (csvdata.Options, error) {
	result := opts.CSV
	if opts.Dialect != "sqlserver" {
		return result, fmt.Errorf("CSV migrations require SQL Server")
	}
	if opts.Bundle != nil || opts.AllowDrops || opts.Routines != "" || len(opts.Variables) > 0 || opts.Source.Content != "" {
		return result, fmt.Errorf("CSV sources cannot be combined with schema bundle options")
	}
	if result.Files == nil {
		if opts.Source.Kind != "csv" {
			return result, fmt.Errorf("captured CSV source files are required")
		}
		var err error
		result.Files, err = BundleFiles(opts.Source.Files)
		if err != nil {
			return result, err
		}
	}
	result.Schema = opts.Schema
	return result, nil
}

func planCSV(ctx context.Context, db *sql.DB, opts Options) (PlanResult, error) {
	input, err := resolveCSV(opts)
	if err != nil {
		return PlanResult{}, err
	}
	artifact, err := csvdata.Plan(ctx, db, input)
	if err != nil {
		return PlanResult{}, err
	}
	body, err := json.Marshal(artifact)
	if err != nil {
		return PlanResult{}, err
	}
	return reviewCSV(body, opts.Dialect, opts.Schema)
}

func reviewCSV(body json.RawMessage, dialect, schema string) (PlanResult, error) {
	var artifact csvdata.Artifact
	if err := Decode(body, &artifact); err != nil {
		return PlanResult{}, err
	}
	if (dialect != "" && dialect != "sqlserver") || (schema != "" && schema != artifact.Schema) {
		return PlanResult{}, fmt.Errorf("CSV artifact does not match target dialect or schema")
	}
	acks := artifact.Acknowledgements()
	if err := artifact.Validate(artifact.Hash, acks); err != nil {
		return PlanResult{}, err
	}
	result := PlanResult{Dialect: "sqlserver", Schema: artifact.Schema, Artifact: body, Original: body, Hash: artifact.Hash,
		Acks: acks, Steps: []Step{}, Applicable: true, CSVPlan: &artifact}
	return result, result.complete()
}
