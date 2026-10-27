package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/task"
	"github.com/flanksource/sqlglot-go/database/compare"
	"github.com/flanksource/sqlglot-go/database/history"
	inspection "github.com/flanksource/sqlglot-go/database/inspect"
	"github.com/flanksource/sqlglot-go/database/inspect/snapshot"
	"github.com/flanksource/sqlglot-go/database/migrate/review"
	"github.com/flanksource/sqlglot-go/database/operation"
	"github.com/spf13/cobra"
)

type InspectOptions struct {
	TargetOptions
	Snapshot bool `json:"snapshot" flag:"snapshot" help:"Return a complete migration snapshot"`
}

type InspectionOutput struct{ Value any }

func (o InspectionOutput) MarshalJSON() ([]byte, error) { return json.Marshal(o.Value) }
func (o InspectionOutput) Pretty() api.Text {
	data, err := json.MarshalIndent(o.Value, "", "  ")
	if err != nil {
		panic(err)
	}
	return clicky.Text(string(data))
}

func addInspectCommand(parent *cobra.Command) {
	cmd := clicky.AddNamedCommandWithContext("inspect", parent, InspectOptions{}, func(ctx context.Context, opts InspectOptions) (InspectionOutput, error) {
		return withHistory(func(store *history.Store) (InspectionOutput, error) {
			return operation.Run(ctx, operation.Options{Run: opts.Run, Kind: history.KindInspection, Target: opts.DSN, Name: "Inspect schema", Store: store}, func(ctx context.Context, record *history.Record, t *task.Task) (InspectionOutput, error) {
				return inspectTarget(ctx, opts, record, t)
			})
		})
	})
	cmd.Short = "Read a catalog or complete migration snapshot"
	cmd.Annotations = map[string]string{"clicky/operation-path": "schema/inspect"}
	jsonCLICommand(cmd)
}

func inspectTarget(ctx context.Context, opts InspectOptions, record *history.Record, t *task.Task) (_ InspectionOutput, returnErr error) {
	conn, err := opts.open(ctx, true)
	if err != nil {
		return InspectionOutput{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, conn.DB.Close()) }()
	record.Dialect, record.Schemas = conn.Dialect, []string{conn.Schema}
	if !opts.Snapshot {
		value, err := inspection.Inspect(ctx, conn.DB, conn.Dialect, inspection.Limits{})
		return InspectionOutput{Value: value}, err
	}
	if conn.Dialect == "sqlserver" {
		value, err := snapshot.Inspect(ctx, conn.DB, snapshot.Options{Schemas: []string{conn.Schema}, Task: t})
		if err == nil {
			record.Summary = compare.SchemaSummary(value.Schema)
		}
		return InspectionOutput{Value: value}, err
	}
	value, err := review.Inspect(ctx, conn.DB, conn.Dialect, conn.Schema)
	if err == nil {
		record.Summary, err = compare.SnapshotSummary(value)
	}
	return InspectionOutput{Value: value}, err
}
