package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"slices"

	scriptserver "github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	"github.com/hashicorp/hcl/v2/hclparse"
)

// Preview is what PreviewBundle plans. For PostgreSQL and SQLite, Artifact is
// the engine's reviewed artifact: its changes (forced rebuilds carry their
// cause), preserved drops, refusals, warnings and units with their ordered
// statements and preconditions. A non-empty Advisory marks a bundle whose
// scripts or security blocks the artifact does not carry, so it must not be
// applied as the whole migration.
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
		plan, err := planSQLServer(ctx, db, files, cfg)
		if err != nil {
			return result, err
		}
		result.ScriptPlan = &plan
		return result, nil
	}
	parser, security, err := loadHCL(files, cfg.dir, cfg.input)
	if err != nil {
		return result, err
	}
	var advisory []string
	if len(scripts) > 0 {
		advisory = append(advisory, "SQL scripts require phased reviewed artifacts")
	}
	if len(security.Roles)+len(security.Permissions) > 0 {
		advisory = append(advisory, "security changes require phased reviewed artifacts")
	}
	artifact, err := previewSchema(ctx, db, dialect, previewInput{files: files, parser: parser, cfg: cfg})
	if err != nil {
		return result, err
	}
	result.Artifact = &artifact
	result.Advisory = slices.Clone(advisory)
	result.Warnings = append(result.Warnings, advisory...)
	return result, nil
}

// previewInput is the bundle a schema preview plans toward.
type previewInput struct {
	files  fs.FS
	parser *hclparse.Parser
	cfg    options
}

// previewSchema plans the bundle's schema against the live database.
func previewSchema(ctx context.Context, db *sql.DB, dialect string, in previewInput) (engine.Artifact, error) {
	if dialect == "sqlserver" {
		return previewSQLServer(ctx, db, in.files, in.cfg)
	}
	if in.cfg.routines != "" {
		return engine.Artifact{}, fmt.Errorf("bundle previews do not support routines")
	}
	var step schemaStep
	var err error
	switch dialect {
	case "sqlite":
		if in.cfg.schema != defaultSchema {
			return engine.Artifact{}, fmt.Errorf("SQLite bundle schema must be public")
		}
		step, err = sqliteStep(in.parser, in.cfg)
	case "postgres":
		step, err = postgresStep(in.parser, in.cfg)
	default:
		err = fmt.Errorf("unsupported bundle preview dialect %q", dialect)
	}
	if err != nil {
		return engine.Artifact{}, err
	}
	return step.plan(ctx, db)
}
