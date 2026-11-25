package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"

	bundle "github.com/flanksource/sqlglot-go/database/migrate"
	"github.com/flanksource/sqlglot-go/database/migrate/dialect"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// Plan plans one migration source against the target and returns its review.
// A schema source (hcl or snapshot) plans in complete scope: every object of
// the target's schema not in the source is dropped, and each drop must be
// acknowledged to apply. A bundle plans in declared scope unless AllowDrops.
func Plan(ctx context.Context, db *sql.DB, opts Options) (PlanResult, error) {
	if len(opts.Labels) > 0 && opts.Source.Kind != "bundle" && opts.Bundle == nil && opts.Project == nil {
		return PlanResult{}, fmt.Errorf("label selectors require %s", ProjectConfigName)
	}
	if opts.Source.Kind == "csv" || opts.CSV.Files != nil {
		return planCSV(ctx, db, opts)
	}
	if opts.Source.Kind == "bundle" && opts.Source.Content != "" {
		return PlanResult{}, fmt.Errorf("bundle sources require files, not content")
	}
	if opts.Source.Kind != "bundle" && len(opts.Source.Files) > 0 {
		return PlanResult{}, fmt.Errorf("files require a bundle source")
	}
	if opts.Source.Kind != "bundle" && opts.Bundle == nil && opts.Project == nil && (len(opts.Variables) > 0 || opts.Routines != "" || opts.AllowDrops) {
		return PlanResult{}, fmt.Errorf("variables, routines and allow-drops require a bundle")
	}
	if opts.Source.Kind == "artifact" {
		return Review([]byte(opts.Source.Content), opts.Dialect, opts.Schema)
	}
	if opts.Source.Kind == "bundle" || opts.Bundle != nil || opts.Project != nil {
		return planBundle(ctx, db, opts)
	}
	return planSchema(ctx, db, opts)
}

// planSchema plans a desired schema given as HCL or as an inspected snapshot.
func planSchema(ctx context.Context, db *sql.DB, opts Options) (PlanResult, error) {
	if opts.Source.Kind != "hcl" && opts.Source.Kind != "snapshot" {
		return PlanResult{}, fmt.Errorf("unknown migration source %q", opts.Source.Kind)
	}
	if strings.TrimSpace(opts.Source.Content) == "" {
		return PlanResult{}, fmt.Errorf("desired content is required")
	}
	desired, err := desiredSnapshot(opts)
	if err != nil {
		return PlanResult{}, err
	}
	artifact, err := engine.PlanLive(ctx, db, engine.PlanLiveRequest{
		Dialect: opts.Dialect, Scope: engine.Scope{Mode: engine.ScopeComplete, Schemas: desired.Schemas}, Desired: desired.Schema,
	})
	if err != nil {
		return PlanResult{}, err
	}
	if err := desired.RefuseTouched(artifact.Changes); err != nil {
		return PlanResult{}, err
	}
	data, err := json.Marshal(artifact)
	if err != nil {
		return PlanResult{}, err
	}
	return Review(data, opts.Dialect, opts.Schema)
}

// desiredSnapshot reads the desired state of the target's schema, which must
// hold nothing outside it.
func desiredSnapshot(opts Options) (dialect.Snapshot, error) {
	if opts.Schema == "" {
		return dialect.Snapshot{}, fmt.Errorf("a target schema is required")
	}
	var desired dialect.Snapshot
	if opts.Source.Kind == "snapshot" {
		if err := Decode([]byte(opts.Source.Content), &desired); err != nil {
			return desired, fmt.Errorf("the desired snapshot is not one that `sqlglot schema inspect --snapshot` writes: %w", err)
		}
		if desired.Dialect != opts.Dialect {
			return desired, fmt.Errorf("the desired snapshot is for %s, not the target's %s", desired.Dialect, opts.Dialect)
		}
	} else {
		schema, err := dialect.DesiredFromHCL(opts.Dialect, opts.Source.Content)
		if err != nil {
			return desired, err
		}
		desired = dialect.Snapshot{Dialect: opts.Dialect, Schemas: []string{opts.Schema}, Schema: schema}
	}
	return desired, desired.CheckWithin([]string{opts.Schema})
}

type planPreview struct {
	bundle.Preview
	Sources []ProjectSource `json:"sources,omitempty"`
}

func planBundle(ctx context.Context, db *sql.DB, opts Options) (PlanResult, error) {
	files, project, err := bundleProject(opts)
	if err != nil {
		return PlanResult{}, err
	}
	var options []bundle.Option
	var sources []ProjectSource
	if project != nil {
		mapped, scripts, inventory, err := project.Bundle()
		if err != nil {
			return PlanResult{}, err
		}
		files, sources = mapped, inventory
		options = append(options, bundle.WithScriptSources(scripts))
	}
	variables, err := Variables(opts.Variables)
	if err != nil {
		return PlanResult{}, err
	}
	options = append(options, bundle.WithVariables(variables), bundle.WithReviewedPlan())
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
	data, err := json.Marshal(planPreview{Preview: preview, Sources: sources})
	if err != nil {
		return PlanResult{}, err
	}
	return Review(data, opts.Dialect, opts.Schema)
}

// bundleProject finds the bundle files and the project mapped over them, with
// opts.Labels applied. A nil project means the files are a plain bundle.
func bundleProject(opts Options) (fs.FS, *Project, error) {
	files, project := opts.Bundle, opts.Project
	if project == nil {
		if files == nil {
			var err error
			if files, err = BundleFiles(opts.Source.Files); err != nil {
				return nil, nil, err
			}
		}
		var err error
		if project, err = LoadProject(files); err != nil {
			return nil, nil, err
		}
	}
	if project == nil && len(opts.Labels) > 0 {
		return nil, nil, fmt.Errorf("label selectors require %s", ProjectConfigName)
	}
	if project == nil {
		return files, nil, nil
	}
	project, err := project.Select(opts.Labels)
	return files, project, err
}
