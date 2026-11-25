package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"slices"

	scriptserver "github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// Preview is what PreviewBundle plans. For PostgreSQL and SQLite, Artifact is
// the engine's reviewed artifact: its changes (forced rebuilds carry their
// cause), preserved drops, refusals, warnings and units with their ordered
// statements and preconditions. Without WithReviewedPlan it is what an
// unreviewed Apply would run: the drops of undeclared columns are not in it
// (Warnings names each one), so it holds no change when nothing else is left to
// run. A non-empty Advisory marks a bundle whose scripts or security blocks the
// artifact does not carry, so it must not be applied as the whole migration.
type Preview struct {
	Artifact   *engine.Artifact       `json:"artifact,omitempty"`
	Advisory   []string               `json:"advisory,omitempty"`
	Scripts    []ScriptAnalysis       `json:"scripts,omitempty"`
	ScriptPlan *scriptserver.PlanFile `json:"scriptPlan,omitempty"`
	Warnings   []string               `json:"warnings,omitempty"`
	Error      string                 `json:"error,omitempty"`
}

// PreviewBundle inspects and plans without executing scripts or creating metadata.
// Script and security bundles yield advisory schema plans until phased artifacts are supported.
func PreviewBundle(ctx context.Context, db *sql.DB, dialect string, files fs.FS, opts ...Option) (result Preview, returnErr error) {
	defer func() {
		if returnErr != nil {
			result.Error = returnErr.Error()
		}
	}()
	if files == nil {
		return result, fmt.Errorf("schema filesystem is nil")
	}
	cfg := resolveOptions(opts)
	if dialect != "sqlserver" {
		if err := refuseScriptPolicy(cfg, dialect); err != nil {
			return result, err
		}
	}
	scripts, err := loadScripts(files, cfg)
	if err != nil {
		return result, err
	}
	for _, name := range scriptPaths(scripts) {
		s := scripts[name]
		a := AnalyzeScript(s.content, dialect)
		a.Path, a.Phase = s.path, string(s.phase)
		result.Scripts = append(result.Scripts, a)
		if s.phase == phasePre && (a.Kind == "ddl" || a.Kind == "mixed" || a.Kind == "unknown") {
			result.Warnings = append(result.Warnings, fmt.Sprintf("pre-script %s is %s; preview uses the current database without executing it", s.path, a.Kind))
		}
	}
	if dialect == "sqlserver" && (len(scripts) > 0 || cfg.scriptPolicy != nil) {
		plan, warnings, err := planSQLServer(ctx, db, files, cfg)
		if err != nil {
			return result, err
		}
		result.ScriptPlan = &plan
		result.Warnings = append(result.Warnings, warnings...)
		return result, nil
	}
	loaded, err := loadHCL(hclSource{FS: files, Dir: cfg.dir, Input: cfg.input, PartialInSource: dialect == "sqlserver"})
	if err != nil {
		return result, err
	}
	var advisory []string
	if len(scripts) > 0 {
		advisory = append(advisory, "SQL scripts require phased reviewed artifacts")
	}
	if len(loaded.Security.Roles)+len(loaded.Security.Permissions) > 0 {
		advisory = append(advisory, "security changes require phased reviewed artifacts")
	}
	selected, err := previewSchema(ctx, db, dialect, previewInput{files: files, hcl: loaded, cfg: cfg})
	if err != nil {
		return result, err
	}
	result.Artifact = selected.Artifact
	result.Advisory = slices.Clone(advisory)
	result.Warnings = append(result.Warnings, advisory...)
	result.Warnings = append(result.Warnings, selected.Warnings...)
	return result, nil
}

// previewInput is the bundle a schema preview plans toward.
type previewInput struct {
	files fs.FS
	hcl   loadedHCL
	cfg   options
}

// previewSchema plans the bundle's schema against the live database: every
// change the scope allows under WithReviewedPlan, otherwise what an unreviewed
// apply would run.
func previewSchema(ctx context.Context, db *sql.DB, dialect string, in previewInput) (unreviewedSelection, error) {
	if dialect == "sqlserver" {
		return previewSQLServer(ctx, db, in.files, in.cfg)
	}
	if in.cfg.routines != "" {
		return unreviewedSelection{}, fmt.Errorf("bundle previews do not support routines")
	}
	var step schemaStep
	var err error
	switch dialect {
	case "sqlite":
		if in.cfg.schema != defaultSchema {
			return unreviewedSelection{}, fmt.Errorf("SQLite bundle schema must be public")
		}
		step, err = sqliteStep(in.hcl, in.cfg)
	case "postgres":
		step, err = postgresStep(in.hcl, in.cfg)
	default:
		err = fmt.Errorf("unsupported bundle preview dialect %q", dialect)
	}
	if err != nil {
		return unreviewedSelection{}, err
	}
	if in.cfg.reviewedPlan {
		full, err := step.plan(ctx, db, changeSelection{})
		return unreviewedSelection{Artifact: &full}, err
	}
	return step.planUnreviewed(ctx, db)
}
