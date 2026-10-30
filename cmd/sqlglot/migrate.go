package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/clicky/task"
	"github.com/flanksource/sqlglot-go/database/history"
	"github.com/flanksource/sqlglot-go/database/migrate/workflow"
	"github.com/flanksource/sqlglot-go/database/operation"
	dbtarget "github.com/flanksource/sqlglot-go/database/target"
	"github.com/spf13/cobra"
)

type TargetOptions struct {
	DSN            string `json:"dsn" flag:"dsn" help:"Connection string or connection:// reference"`
	Driver         string `json:"driver,omitempty" flag:"driver" help:"Driver override, matching the connection"`
	Schema         string `json:"schema,omitempty" flag:"schema" help:"Schema (defaults to main, public or dbo)"`
	ConnectionsDir string `json:"connections-dir,omitempty" flag:"connections-dir" help:"Directory of named connections"`
	Run            string `json:"run,omitempty" flag:"run" help:"Unique run ID for history and task progress"`
	Parent         string `json:"parent,omitempty" flag:"parent" help:"Recorded parent run ID"`
}

func (o TargetOptions) open(ctx context.Context, readOnly bool) (dbtarget.Connection, error) {
	dir := o.ConnectionsDir
	if dir == "" {
		dir = compareConnectionsDir
	}
	return dbtarget.Open(ctx, dbtarget.OpenOptions{DSN: o.DSN, Driver: o.Driver, Schema: o.Schema, ConnectionsDir: dir, ReadOnly: readOnly})
}

type MigrationOptions struct {
	TargetOptions
	Bundle          string          `json:"bundle,omitempty" flag:"bundle" help:"Directory containing HCL and SQL files (CLI only)"`
	Desired         string          `json:"desired,omitempty" flag:"desired" help:"Desired HCL or snapshot JSON file (CLI only)"`
	Output          string          `json:"out,omitempty" flag:"out" help:"Save the reviewed artifact to a file (CLI only)"`
	Artifact        string          `json:"artifact,omitempty" flag:"artifact" help:"Reviewed artifact file (CLI only)"`
	Hash            string          `json:"hash,omitempty" flag:"hash" help:"Explicitly reviewed artifact hash"`
	Acks            []string        `json:"ack,omitempty" flag:"ack" split:"false" help:"Acknowledged destructive step IDs"`
	LockTimeout     string          `json:"lock-timeout,omitempty" flag:"lock-timeout" default:"5s" help:"Maximum migration lock wait"`
	Variables       []string        `json:"var,omitempty" flag:"var" split:"false" help:"Bundle variable as name=HCL-literal"`
	Routines        string          `json:"routines,omitempty" flag:"routines" help:"Routine directory within the bundle"`
	AllowDrops      bool            `json:"allow-drops,omitempty" flag:"allow-drops" help:"Include omitted objects as removals in a bundle"`
	Source          workflow.Source `json:"source" flag:"-"`
	ArtifactContent json.RawMessage `json:"artifactContent,omitempty" flag:"-"`
}

type PlanOutput struct{ workflow.PlanResult }

func (p PlanOutput) Pretty() api.Text {
	var value any
	if err := json.Unmarshal(p.Original, &value); err != nil {
		panic(err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		panic(err)
	}
	return clicky.Text(string(data))
}

type ApplyOutput struct{ Value any }

func (o ApplyOutput) MarshalJSON() ([]byte, error) { return json.Marshal(o.Value) }
func (o ApplyOutput) Pretty() api.Text {
	data, err := json.MarshalIndent(o.Value, "", "  ")
	if err != nil {
		panic(err)
	}
	return clicky.Text(string(data))
}

func newMigrationPlanCommand() *cobra.Command {
	parent := &cobra.Command{Use: "migrate"}
	cmd := clicky.AddNamedCommandWithContext("plan", parent, MigrationOptions{}, func(ctx context.Context, opts MigrationOptions) (result PlanOutput, returnErr error) {
		opts, err := migrationInput(ctx, opts)
		if err != nil {
			return PlanOutput{}, err
		}
		payload, err := migrateRun(ctx, opts, "plan")
		if err != nil {
			return PlanOutput{}, err
		}
		if opts.Output != "" {
			root, err := os.OpenRoot(filepath.Dir(opts.Output))
			if err != nil {
				return PlanOutput{}, err
			}
			defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
			if err := root.WriteFile(filepath.Base(opts.Output), payload.Plan.Original, 0600); err != nil {
				return PlanOutput{}, err
			}
		}
		return PlanOutput{*payload.Plan}, nil
	})
	cmd.Short = "Generate a reviewed artifact without changing the database"
	jsonCLICommand(cmd)
	return cmd
}

func newMigrationApplyCommand() *cobra.Command {
	parent := &cobra.Command{Use: "migrate"}
	cmd := clicky.AddNamedCommandWithContext("apply", parent, MigrationOptions{}, func(ctx context.Context, opts MigrationOptions) (ApplyOutput, error) {
		opts, err := migrationInput(ctx, opts)
		if err != nil {
			return ApplyOutput{}, err
		}
		if opts.Hash == "" {
			return ApplyOutput{}, fmt.Errorf("reviewed --hash is required")
		}
		payload, err := migrateRun(ctx, opts, "apply")
		return ApplyOutput{Value: payload.Result}, err
	})
	cmd.Short = "Apply exact reviewed SQL with hash and destructive acknowledgements"
	jsonCLICommand(cmd)
	return cmd
}

func newMigrationReviewCommand() *cobra.Command {
	parent := &cobra.Command{Use: "migrate"}
	cmd := clicky.AddNamedCommandWithContext("review", parent, MigrationOptions{}, func(ctx context.Context, opts MigrationOptions) (PlanOutput, error) {
		opts, err := migrationInput(ctx, opts)
		if err != nil {
			return PlanOutput{}, err
		}
		plan, err := workflow.Review(opts.ArtifactContent, opts.Driver, opts.Schema)
		return PlanOutput{PlanResult: plan}, err
	})
	cmd.Short = "Review an uploaded artifact without opening a database"
	jsonCLICommand(cmd)
	return cmd
}

func migrationInput(ctx context.Context, opts MigrationOptions) (MigrationOptions, error) {
	if request, ok := rpc.RequestFromContext(ctx); ok {
		if err := decodeRequest(request, &opts); err != nil {
			return opts, err
		}
		if opts.Bundle != "" || opts.Desired != "" || opts.Artifact != "" || opts.Output != "" || opts.ConnectionsDir != "" {
			return opts, entity.NewStatusError(http.StatusBadRequest, "invalid_input", "HTTP requests require uploaded content, not server paths")
		}
		return opts, nil
	}
	if (opts.Bundle != "" && (opts.Desired != "" || opts.Artifact != "")) || (opts.Desired != "" && opts.Artifact != "") {
		return opts, fmt.Errorf("select exactly one source")
	}
	if opts.Desired != "" {
		data, err := os.ReadFile(opts.Desired)
		if err != nil {
			return opts, err
		}
		opts.Source = workflow.Source{Kind: "hcl", Content: string(data)}
		if json.Valid(data) {
			opts.Source.Kind = "snapshot"
		}
	}
	if opts.Artifact != "" {
		data, err := os.ReadFile(opts.Artifact)
		if err != nil {
			return opts, err
		}
		opts.ArtifactContent = data
	}
	return opts, nil
}

func migrateRun(ctx context.Context, opts MigrationOptions, mode string) (workflow.RunResult, error) {
	return withHistory(func(store *history.Store) (workflow.RunResult, error) {
		return operation.Run(ctx, operation.Options{Run: opts.Run, Parent: opts.Parent, Kind: history.KindMigration, Target: opts.DSN, Name: "Migration " + mode, Store: store}, func(ctx context.Context, record *history.Record, t *task.Task) (result workflow.RunResult, returnErr error) {
			dir := opts.ConnectionsDir
			if dir == "" {
				dir = compareConnectionsDir
			}
			plan := workflow.Options{Source: opts.Source, Variables: opts.Variables, Routines: opts.Routines, AllowDrops: opts.AllowDrops}
			if opts.Bundle != "" {
				root, err := os.OpenRoot(opts.Bundle)
				if err != nil {
					return workflow.RunResult{}, err
				}
				defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
				plan.Bundle = root.FS()
			}
			return workflow.Execute(ctx, workflow.RunOptions{Mode: mode, Target: dbtarget.OpenOptions{DSN: opts.DSN, Driver: opts.Driver, Schema: opts.Schema, ConnectionsDir: dir}, Plan: plan, Hash: opts.Hash, Acks: opts.Acks, ArtifactContent: opts.ArtifactContent, LockTimeout: opts.LockTimeout}, record, t)
		})
	})
}
