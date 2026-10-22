package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"slices"

	"ariga.io/atlas/sql/postgres"
	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlite"
	"github.com/flanksource/sqlglot-go/database/migrate/review"
	native "github.com/flanksource/sqlglot-go/migrate"
)

type Preview struct {
	Artifact          *review.Artifact `json:"artifact,omitempty"`
	SQLServerArtifact *native.Artifact `json:"sqlServerArtifact,omitempty"`
	SQLServerPlan     *native.Plan     `json:"sqlServerPlan,omitempty"`
	Scripts           []ScriptAnalysis `json:"scripts,omitempty"`
	Warnings          []string         `json:"warnings,omitempty"`
	Error             string           `json:"error,omitempty"`
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
	scripts, err := loadScripts(files, cfg.dir, cfg.routines)
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
	if dialect == "sqlserver" {
		return previewSQLServer(ctx, db, files, cfg, result, advisory)
	}
	if cfg.routines != "" || len(cfg.exclude) > 0 {
		return result, fmt.Errorf("reviewed bundle previews do not support routines or inspection exclusions")
	}
	realm := &schema.Realm{}
	if err = postgres.EvalHCL.Eval(parser, realm, cfg.input); err != nil {
		return result, fmt.Errorf("evaluate preview HCL: %w", err)
	}
	schemaName := cfg.schema
	var desired []byte
	switch dialect {
	case "sqlite":
		if cfg.schema != defaultSchema {
			return result, fmt.Errorf("SQLite bundle schema must be public")
		}
		if _, err = projectSQLiteRealm(realm); err != nil {
			return result, err
		}
		schemaName = "main"
		desired, err = sqlite.MarshalHCL.MarshalSpec(realm)
	case "postgres":
		if err = remapDesiredSchema(realm, cfg.schema); err != nil {
			return result, err
		}
		desired, err = postgres.MarshalHCL.MarshalSpec(realm)
	default:
		return result, fmt.Errorf("unsupported reviewed bundle dialect %q", dialect)
	}
	if err != nil {
		return result, err
	}
	artifact, err := review.Build(ctx, db, review.Options{Dialect: dialect, Schema: schemaName, DesiredHCL: string(desired), Advisory: advisory, PreserveOmitted: !cfg.allowDrops})
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	result.Artifact = &artifact
	result.Warnings = append(result.Warnings, slices.Clone(advisory)...)
	return result, nil
}
