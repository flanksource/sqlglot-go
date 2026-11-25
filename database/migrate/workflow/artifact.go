package workflow

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/flanksource/sqlglot-go/database/migrate/csvdata"
	scriptplan "github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// Review reads a plan file and reports what applying it does. It opens no
// database: a schema artifact is checked against its own hash and re-planned
// from its own snapshots, so an edited statement, unit or change is refused.
func Review(data []byte, dialect, schema string) (PlanResult, error) {
	pf, err := readPlanFile(data)
	if err != nil {
		return PlanResult{}, err
	}
	switch {
	case pf.script != nil:
		result, err := reviewScriptPlan(data, pf.script, pf.envelope.Warnings, dialect, schema)
		result.Sources = pf.envelope.Sources
		return result, err
	case pf.csv != nil:
		return reviewCSV(pf.body, dialect, schema)
	}
	if err := matchTarget(*pf.schema, dialect, schema); err != nil {
		return PlanResult{}, err
	}
	if err := engine.Validate(*pf.schema); err != nil {
		return PlanResult{}, err
	}
	return schemaResult(pf)
}

// Validate checks a plan file's integrity and acknowledgements without opening
// a database. Each artifact carries its own hash, which guards only that the
// file is what its planner wrote.
func Validate(opts ApplyOptions) error {
	pf, err := readPlanFile(opts.Artifact)
	if err != nil {
		return err
	}
	switch {
	case pf.script != nil:
		return pf.script.Verify()
	case pf.csv != nil:
		return pf.csv.Validate(pf.csv.Hash, opts.Acks)
	}
	if len(pf.envelope.Advisory) > 0 {
		return fmt.Errorf("an advisory preview cannot be applied: %s", strings.Join(pf.envelope.Advisory, "; "))
	}
	if err := matchTarget(*pf.schema, opts.Dialect, opts.Schema); err != nil {
		return err
	}
	return engine.ValidateRequest(applyRequest(*pf.schema, opts))
}

func applyRequest(a engine.Artifact, opts ApplyOptions) engine.ApplyRequest {
	return engine.ApplyRequest{Artifact: a, Hash: a.Hash, Acknowledgements: opts.Acks, LockTimeout: opts.LockTimeout}
}

// Apply validates a plan file and applies it. A schema artifact runs unit by
// unit, each in its own transaction where the dialect has them, so a failed
// run keeps the units before it and a second apply resumes after them.
func Apply(ctx context.Context, db *sql.DB, opts ApplyOptions) (any, error) {
	if err := Validate(opts); err != nil {
		return nil, err
	}
	pf, err := readPlanFile(opts.Artifact)
	if err != nil {
		return nil, err
	}
	switch {
	case pf.script != nil:
		if opts.Dialect != "sqlserver" {
			return nil, fmt.Errorf("script plans require SQL Server")
		}
		return scriptplan.Apply(ctx, db, *pf.script)
	case pf.csv != nil:
		if opts.Dialect != "sqlserver" {
			return nil, fmt.Errorf("CSV migrations require SQL Server")
		}
		return csvdata.Apply(ctx, db, csvdata.ApplyOptions{Options: opts.CSV, Artifact: *pf.csv, Hash: pf.csv.Hash, Acks: opts.Acks, LockTimeout: opts.LockTimeout})
	}
	result, err := engine.Apply(ctx, db, applyRequest(*pf.schema, opts))
	return applyResult(result), err
}

func applyResult(result engine.Result) ApplyResult {
	out := ApplyResult{Hash: result.Hash, Outcome: result.Outcome, Units: result.Units}
	for _, u := range result.Units {
		out.Steps += u.Statements
	}
	return out
}
