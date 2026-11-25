package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/clicky/task"
	"github.com/flanksource/sqlglot-go/database/history"
	"github.com/flanksource/sqlglot-go/database/migrate/csvdata"
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
	Target          string            `json:"target,omitempty" flag:"target" help:"Target alias defined in .sqlglot-go.yaml"`
	Bundle          string            `json:"bundle,omitempty" flag:"bundle" help:"Directory containing HCL and SQL files (CLI only)"`
	CSVRoot         string            `json:"csv-root,omitempty" flag:"csv-root" help:"Directory containing CSV migration sources (CLI only)"`
	Country         []string          `json:"country,omitempty" flag:"-" help:"CSV country layer filter"`
	Environment     []string          `json:"env,omitempty" flag:"-" help:"CSV environment layer filter"`
	Layer           []string          `json:"layer,omitempty" flag:"layer" split:"false" help:"Filter a CSV layer as NAME=FILTER (repeatable)"`
	LayerFilters    map[string]string `json:"layerFilters,omitempty" flag:"-"`
	CSVPhase        string            `json:"csvPhase,omitempty" flag:"csv-phase" help:"Select pre-rules or post-rules CSV tables (both by default)"`
	Desired         string            `json:"desired,omitempty" flag:"desired" help:"Desired HCL or snapshot JSON file (CLI only)"`
	Output          string            `json:"out,omitempty" flag:"out" help:"Plan file to write (CLI only, default .sqlglot-go/plan.json)"`
	Paths           []string          `json:"paths,omitempty" args:"true" split:"false" help:"Plan sources: SQL or HCL files and directories, each file detected by its content (plan); the plan file (apply, review). CLI only"`
	Acks            []string          `json:"ack,omitempty" flag:"ack" split:"false" help:"Acknowledged destructive change IDs, as listed in the plan's acks"`
	LockTimeout     string            `json:"lock-timeout,omitempty" flag:"lock-timeout" default:"5s" help:"Maximum migration lock wait"`
	Labels          []string          `json:"label,omitempty" flag:"label" split:"false" help:"Select project sources by label as key=pattern or !key=pattern (repeatable, replaces the project select)"`
	Variables       []string          `json:"var,omitempty" flag:"var" split:"false" help:"Bundle variable as name=HCL-literal"`
	Routines        string            `json:"routines,omitempty" flag:"routines" help:"Routine directory within the bundle"`
	AllowDrops      bool              `json:"allow-drops,omitempty" flag:"allow-drops" help:"Include omitted objects as removals in a bundle"`
	Source          workflow.Source   `json:"source" flag:"-"`
	ArtifactContent json.RawMessage   `json:"artifactContent,omitempty" flag:"-"`
	project         *workflow.Project
}

// defaultPlanPath is where migrate plan writes its plan file without --out.
const defaultPlanPath = ".sqlglot-go/plan.json"

type PlanOutput struct{ workflow.PlanResult }

// Pretty summarises what applying the plan does: a script plan as its plan
// file renders it, any other artifact as its steps.
func (p PlanOutput) Pretty() api.Text {
	if p.ScriptPlan != nil {
		return p.ScriptPlan.Pretty()
	}
	out := clicky.Text("Plan for "+p.Dialect+" schema "+p.Schema, "font-bold")
	destructive := 0
	for _, step := range p.Steps {
		marker, style := "  + ", "text-green-600"
		if step.Destructive {
			destructive++
			marker, style = "  ! destructive ", "text-red-600"
		}
		out = out.NewLine().Append(marker, style).Append(strings.TrimSpace(step.SQL))
	}
	for _, change := range p.Changes {
		for _, rebuild := range change.Rebuilds {
			out = out.NewLine().Append("  ~ ", "text-blue-600").Append(rebuild.Reason)
		}
	}
	for _, unit := range p.Units {
		for _, check := range unit.Preconditions {
			out = out.NewLine().Append("  ? checked first: ", "text-blue-600").Append(check.Message)
		}
	}
	for _, change := range p.Preserved {
		out = out.NewLine().Append("  - left alone: ", "text-muted").Append(change.ID)
	}
	for _, warning := range p.Warnings {
		out = out.NewLine().Append("  ! ", "text-yellow-600").Append(warning)
	}
	if !p.Applicable {
		out = out.NewLine().Append("This plan is advisory and cannot be applied.", "text-yellow-600")
	}
	return out.NewLine().Append(fmt.Sprintf("Plan: %d steps (%d destructive).", len(p.Steps), destructive), "font-bold")
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
	cmd := clicky.AddNamedCommandWithContext("plan", parent, MigrationOptions{}, func(ctx context.Context, opts MigrationOptions) (PlanOutput, error) {
		opts, err := migrationInput(ctx, opts, pathsAreSources)
		if err != nil {
			return PlanOutput{}, err
		}
		payload, err := migrateRun(ctx, opts, "plan")
		if err != nil {
			return PlanOutput{}, err
		}
		if _, served := rpc.RequestFromContext(ctx); !served {
			if err := writePlanFile(cmp.Or(opts.Output, defaultPlanPath), payload.Plan.Original); err != nil {
				return PlanOutput{}, err
			}
		}
		return PlanOutput{*payload.Plan}, nil
	})
	cmd.Short = "Plan a migration into a plan file without changing the database"
	cmd.Long = migrationPlanHelp
	jsonCLICommand(cmd)
	return cmd
}

func newMigrationApplyCommand() *cobra.Command {
	parent := &cobra.Command{Use: "migrate"}
	cmd := clicky.AddNamedCommandWithContext("apply", parent, MigrationOptions{}, func(ctx context.Context, opts MigrationOptions) (ApplyOutput, error) {
		opts, err := migrationInput(ctx, opts, pathIsPlanFile)
		if err != nil {
			return ApplyOutput{}, err
		}
		if len(opts.ArtifactContent) == 0 {
			return ApplyOutput{}, fmt.Errorf("a plan file is required: sqlglot migrate apply %s", defaultPlanPath)
		}
		payload, err := migrateRun(ctx, opts, "apply")
		return ApplyOutput{Value: payload.Result}, err
	})
	cmd.Short = "Apply a plan file, refusing it when the target changed since the plan"
	cmd.Long = migrationApplyHelp
	jsonCLICommand(cmd)
	return cmd
}

func newMigrationReviewCommand() *cobra.Command {
	parent := &cobra.Command{Use: "migrate"}
	cmd := clicky.AddNamedCommandWithContext("review", parent, MigrationOptions{}, func(ctx context.Context, opts MigrationOptions) (PlanOutput, error) {
		opts, err := migrationInput(ctx, opts, pathIsPlanFile)
		if err != nil {
			return PlanOutput{}, err
		}
		if len(opts.ArtifactContent) == 0 {
			return PlanOutput{}, fmt.Errorf("a plan file is required: sqlglot migrate review %s", defaultPlanPath)
		}
		plan, err := workflow.Review(opts.ArtifactContent, opts.Driver, opts.Schema)
		return PlanOutput{PlanResult: plan}, err
	})
	cmd.Short = "Review a plan file without opening a database"
	cmd.Long = migrationReviewHelp
	jsonCLICommand(cmd)
	return cmd
}

// pathRole is what a migrate command reads its positional paths as.
type pathRole int

const (
	// pathsAreSources: migrate plan's SQL and HCL files and directories.
	pathsAreSources pathRole = iota
	// pathIsPlanFile: the one plan file migrate apply and review read.
	pathIsPlanFile
)

func migrationInput(ctx context.Context, opts MigrationOptions, role pathRole) (MigrationOptions, error) {
	if request, ok := rpc.RequestFromContext(ctx); ok {
		if err := decodeRequest(request, &opts); err != nil {
			return opts, err
		}
		if opts.Bundle != "" || opts.CSVRoot != "" || opts.Desired != "" || len(opts.Paths) > 0 || opts.Output != "" || opts.ConnectionsDir != "" {
			return opts, entity.NewStatusError(http.StatusBadRequest, "invalid_input", "HTTP requests require uploaded content, not server paths")
		}
		if opts.DSN != "" && opts.Target != "" {
			return opts, entity.NewStatusError(http.StatusBadRequest, "invalid_input", "select either --dsn or --target")
		}
		err := loadHTTPMigrationProject(&opts, role, request)
		return opts, err
	}
	if opts.DSN != "" && opts.Target != "" {
		return opts, fmt.Errorf("select either --dsn or --target")
	}
	sources := 0
	for _, set := range []bool{opts.CSVRoot != "", opts.Bundle != "", opts.Desired != "", role == pathsAreSources && len(opts.Paths) > 0} {
		if set {
			sources++
		}
	}
	if sources > 1 {
		return opts, fmt.Errorf("select exactly one migration source: source paths, --bundle, --desired or --csv-root")
	}
	if role == pathIsPlanFile && (opts.Bundle != "" || opts.Desired != "" || opts.CSVRoot != "") {
		return opts, fmt.Errorf("apply and review read their sources from the plan file; pass only the plan file")
	}
	if role == pathIsPlanFile && len(opts.Labels) > 0 {
		return opts, fmt.Errorf("--label selects sources when planning; a plan file already holds its selection")
	}
	if role == pathIsPlanFile && len(opts.Paths) > 1 {
		return opts, fmt.Errorf("expected one plan file, got %d paths", len(opts.Paths))
	}
	if err := loadCLIMigrationProject(&opts, role); err != nil || opts.project != nil {
		return opts, err
	}
	if role == pathsAreSources && len(opts.Paths) > 0 {
		files, err := readBundleSources(opts.Paths, os.Stderr)
		if err != nil {
			return opts, err
		}
		opts.Source = workflow.Source{Kind: "bundle", Files: files}
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
	if role == pathIsPlanFile && len(opts.Paths) == 1 {
		data, err := os.ReadFile(opts.Paths[0])
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
			plan := workflow.Options{Source: opts.Source, Project: opts.project, Labels: opts.Labels, Variables: opts.Variables, Routines: opts.Routines, AllowDrops: opts.AllowDrops}
			filters, err := migrationLayerFilters(opts)
			if err != nil {
				return workflow.RunResult{}, err
			}
			plan.CSV = csvdata.Options{Filters: filters, Phase: opts.CSVPhase}
			if opts.CSVRoot != "" {
				root, err := os.OpenRoot(opts.CSVRoot)
				if err != nil {
					return workflow.RunResult{}, err
				}
				defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
				plan.CSV.Files = root.FS()
			} else if opts.Source.Kind != "csv" && (len(filters) != 0 || opts.CSVPhase != "") {
				return workflow.RunResult{}, fmt.Errorf("layer filters and phase require a CSV migration source")
			}
			if opts.Bundle != "" && opts.project == nil {
				root, err := os.OpenRoot(opts.Bundle)
				if err != nil {
					return workflow.RunResult{}, err
				}
				defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
				plan.Bundle = root.FS()
			}
			return workflow.Execute(ctx, workflow.RunOptions{Mode: mode, Target: dbtarget.OpenOptions{DSN: opts.DSN, Driver: opts.Driver, Schema: opts.Schema, ConnectionsDir: dir}, Plan: plan, Acks: opts.Acks, ArtifactContent: opts.ArtifactContent, LockTimeout: opts.LockTimeout}, record, t)
		})
	})
}

func migrationLayerFilters(opts MigrationOptions) (map[string]string, error) {
	result := make(map[string]string, len(opts.LayerFilters))
	for name, expression := range opts.LayerFilters {
		result[name] = expression
	}
	inputs := append([]string{}, opts.Layer...)
	for _, expression := range opts.Country {
		inputs = append(inputs, "country="+expression)
	}
	for _, expression := range opts.Environment {
		inputs = append(inputs, "env="+expression)
	}
	for _, input := range inputs {
		name, expression, ok := strings.Cut(input, "=")
		if !ok || name == "" || expression == "" {
			return nil, fmt.Errorf("CSV layer filters require name=values")
		}
		if _, exists := result[name]; exists {
			return nil, fmt.Errorf("duplicate CSV layer filter %q", name)
		}
		result[name] = expression
	}
	return result, nil
}

// writePlanFile writes data to path, creating its directory, and refuses a
// path whose final element escapes that directory.
func writePlanFile(path string, data []byte) (returnErr error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create plan file directory: %w", err)
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
	return root.WriteFile(filepath.Base(path), data, 0600)
}
