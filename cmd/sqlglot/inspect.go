package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/task"
	"github.com/flanksource/sqlglot-go/database/history"
	inspection "github.com/flanksource/sqlglot-go/database/inspect"
	dialects "github.com/flanksource/sqlglot-go/database/migrate/dialect"
	"github.com/flanksource/sqlglot-go/database/operation"
	"github.com/spf13/cobra"
)

type InspectOptions struct {
	TargetOptions
	Snapshot        bool     `json:"snapshot" flag:"snapshot" help:"Return a complete migration snapshot"`
	HCL             bool     `json:"hcl,omitempty" flag:"hcl" help:"Write the tables as SQL Server HCL that sqlglot migrate reads back"`
	Tables          []string `json:"table,omitempty" flag:"table" help:"Table to write as HCL (repeatable; default every table of the schema)"`
	Columns         []string `json:"columns,omitempty" flag:"columns" split:"false" help:"Declare only some columns of a table as TABLE=COLUMN,COLUMN (repeatable), without keys or indexes, and write partial = true in its block"`
	Out             string   `json:"out,omitempty" flag:"out" help:"Directory to write the HCL into, one file per table (CLI only; default one document on stdout)"`
	Document        bool     `json:"document,omitempty" flag:"document" help:"With --out, write one schema.hcl instead of one file per table"`
	OmitUnsupported bool     `json:"omit-unsupported,omitempty" flag:"omit-unsupported" help:"Leave out what HCL cannot declare, with a comment line each, instead of failing"`
}

//go:embed help/inspect.md
var inspectHelp string

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
			return operation.Run(ctx, operation.Options{Run: opts.Run, Kind: history.KindInspection, Target: opts.DSN, Name: "Inspect schema", Store: store}, func(ctx context.Context, record *history.Record, _ *task.Task) (InspectionOutput, error) {
				return inspectTarget(ctx, opts, record)
			})
		})
	})
	cmd.Short = "Read a catalog, a complete migration snapshot or SQL Server tables as HCL"
	cmd.Long = inspectHelp
	cmd.Annotations = map[string]string{"clicky/operation-path": "schema/inspect"}
	jsonCLICommand(cmd)
}

func inspectTarget(ctx context.Context, opts InspectOptions, record *history.Record) (_ InspectionOutput, returnErr error) {
	conn, err := opts.open(ctx, true)
	if err != nil {
		return InspectionOutput{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, conn.DB.Close()) }()
	record.Dialect, record.Schemas = conn.Dialect, []string{conn.Schema}
	if opts.HCL {
		return exportHCL(ctx, conn, opts)
	}
	if flag := opts.hclFlagsWithoutHCL(); flag != "" {
		return InspectionOutput{}, fmt.Errorf("%s writes HCL: add --hcl", flag)
	}
	if !opts.Snapshot {
		value, err := inspection.Inspect(ctx, conn.DB, conn.Dialect, inspection.Limits{})
		return InspectionOutput{Value: value}, err
	}
	value, err := dialects.Inspect(ctx, conn.DB, conn.Dialect, []string{conn.Schema})
	if err == nil {
		record.Summary = value.Summary()
	}
	return InspectionOutput{Value: value}, err
}
