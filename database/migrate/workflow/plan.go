package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/sqlglot-go/database/inspect/snapshot"
	bundle "github.com/flanksource/sqlglot-go/database/migrate"
	"github.com/flanksource/sqlglot-go/database/migrate/review"
	native "github.com/flanksource/sqlglot-go/migrate"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
)

func Plan(ctx context.Context, db *sql.DB, opts Options) (PlanResult, error) {
	if opts.Source.Kind == "bundle" && opts.Source.Content != "" {
		return PlanResult{}, fmt.Errorf("bundle sources require files, not content")
	}
	if opts.Source.Kind != "bundle" && len(opts.Source.Files) > 0 {
		return PlanResult{}, fmt.Errorf("files require a bundle source")
	}
	if opts.Source.Kind != "bundle" && opts.Bundle == nil && (len(opts.Variables) > 0 || opts.Routines != "" || opts.AllowDrops) {
		return PlanResult{}, fmt.Errorf("variables, routines and allow-drops require a bundle")
	}
	if opts.Source.Kind == "artifact" {
		return Review([]byte(opts.Source.Content), opts.Dialect, opts.Schema)
	}
	if opts.Source.Kind == "bundle" || opts.Bundle != nil {
		return planBundle(ctx, db, opts)
	}
	if opts.Source.Kind != "hcl" && opts.Source.Kind != "snapshot" {
		return PlanResult{}, fmt.Errorf("unknown migration source %q", opts.Source.Kind)
	}
	if strings.TrimSpace(opts.Source.Content) == "" {
		return PlanResult{}, fmt.Errorf("desired content is required")
	}
	var artifact any
	var err error
	if opts.Dialect == "sqlserver" {
		artifact, err = planSQLServer(ctx, db, opts)
	} else {
		text := opts.Source.Content
		if opts.Source.Kind == "snapshot" {
			var desired review.Snapshot
			if err = Decode([]byte(text), &desired); err != nil {
				return PlanResult{}, err
			}
			if desired.Dialect != opts.Dialect || desired.Schema != opts.Schema {
				return PlanResult{}, fmt.Errorf("desired snapshot dialect or schema does not match target")
			}
			text = desired.HCL
		}
		artifact, err = review.Build(ctx, db, review.Options{Dialect: opts.Dialect, Schema: opts.Schema, DesiredHCL: text})
	}
	if err != nil {
		return PlanResult{}, err
	}
	data, err := json.Marshal(artifact)
	if err != nil {
		return PlanResult{}, err
	}
	return Review(data, opts.Dialect, opts.Schema)
}

func planBundle(ctx context.Context, db *sql.DB, opts Options) (PlanResult, error) {
	files := opts.Bundle
	if files == nil {
		var err error
		files, err = BundleFiles(opts.Source.Files)
		if err != nil {
			return PlanResult{}, err
		}
	}
	variables, err := Variables(opts.Variables)
	if err != nil {
		return PlanResult{}, err
	}
	options := []bundle.Option{bundle.WithVariables(variables)}
	if opts.Schema != "main" {
		options = append(options, bundle.WithSchema(opts.Schema))
	}
	if opts.AllowDrops {
		options = append(options, bundle.WithDrops())
	}
	if opts.Routines != "" {
		options = append(options, bundle.WithRoutines(opts.Routines))
	}
	preview, err := bundle.PreviewBundle(ctx, db, opts.Dialect, files, options...)
	if err != nil {
		return PlanResult{}, err
	}
	data, err := json.Marshal(preview)
	if err != nil {
		return PlanResult{}, err
	}
	return Review(data, opts.Dialect, opts.Schema)
}

func planSQLServer(ctx context.Context, db *sql.DB, opts Options) (native.Artifact, error) {
	if opts.Source.Kind != "snapshot" {
		return native.Artifact{}, fmt.Errorf("SQL Server requires a complete snapshot JSON")
	}
	var desired snapshot.Result
	if err := Decode([]byte(opts.Source.Content), &desired); err != nil {
		return native.Artifact{}, err
	}
	current, err := snapshot.Inspect(ctx, db, snapshot.Options{Schemas: []string{opts.Schema}})
	if err != nil {
		return native.Artifact{}, err
	}
	return planner.BuildArtifact(native.ArtifactRequest{Schemas: current.Schemas, Request: native.Request{Current: current.Schema, Desired: desired.Schema}})
}
