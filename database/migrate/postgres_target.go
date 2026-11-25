package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"ariga.io/atlas/sql/postgres"
	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/database/migrate/dialect/atlasdriver"
	pgdialect "github.com/flanksource/sqlglot-go/database/migrate/dialect/postgres"
)

// metadataTables are the tables the bundle itself keeps in the migration
// schema. They are never declared, so under WithDrops they would otherwise be
// planned for removal.
var metadataTables = []string{"schema_migration_scripts", "schema_migration_security"}

func validatePostgresApply(connection string, schemaFS fs.FS, cfg options) error {
	if err := refuseScriptPolicy(cfg, "PostgreSQL"); err != nil {
		return err
	}
	if strings.TrimSpace(connection) == "" {
		return errors.New("connection string is empty")
	}
	return validatePostgresBundle(schemaFS, cfg)
}

func validatePostgresBundle(schemaFS fs.FS, cfg options) error {
	if schemaFS == nil {
		return errors.New("schema filesystem is nil")
	}
	if err := ValidateSchemaName(cfg.schema); err != nil {
		return fmt.Errorf("migration schema: %w", err)
	}
	return refuseRoutines(cfg, "PostgreSQL")
}

// postgresBundle is everything a PostgreSQL bundle declares, read and checked
// before the database is touched.
type postgresBundle struct {
	scripts  map[string]*script
	security securitySpec
	step     schemaStep
}

func loadPostgresBundle(schemaFS fs.FS, cfg options) (postgresBundle, error) {
	scripts, err := loadScripts(schemaFS, cfg)
	if err != nil {
		return postgresBundle{}, err
	}
	loaded, err := loadHCL(hclSource{FS: schemaFS, Dir: cfg.dir, Input: cfg.input})
	if err != nil {
		return postgresBundle{}, err
	}
	step, err := postgresStep(loaded, cfg)
	if err != nil {
		return postgresBundle{}, err
	}
	return postgresBundle{scripts: scripts, security: remapSecuritySchema(loaded.Security, cfg.schema), step: step}, nil
}

// postgresStep evaluates the HCL (variables included), moves its public schema
// to the target one and plans toward the result.
func postgresStep(loaded loadedHCL, cfg options) (schemaStep, error) {
	realm := &schema.Realm{}
	if err := postgres.EvalHCL.Eval(loaded.Parser, realm, cfg.input); err != nil {
		return schemaStep{}, fmt.Errorf("evaluate HCL schemas: %w", err)
	}
	if err := remapDesiredSchema(realm, cfg.schema); err != nil {
		return schemaStep{}, fmt.Errorf("scope desired schema: %w", err)
	}
	if len(realm.Schemas) != 1 {
		return schemaStep{}, fmt.Errorf("expected one schema, got %d", len(realm.Schemas))
	}
	if err := atlasdriver.Exclude(realm.Schemas[0], cfg.exclude); err != nil {
		return schemaStep{}, err
	}
	desired, err := pgdialect.SchemaFromAtlas(realm.Schemas[0])
	if err != nil {
		return schemaStep{}, err
	}
	return schemaStep{
		Dialect: pgdialect.Name, Schema: cfg.schema, Desired: atlasdriver.WithPartial(desired, loaded.Partial),
		AllowDrops: cfg.allowDrops, Exclude: append(append([]string(nil), cfg.exclude...), metadataTables...),
	}, nil
}

func applyPostgres(ctx context.Context, connection string, schemaFS fs.FS, cfg options) (returnErr error) {
	if err := validatePostgresApply(connection, schemaFS, cfg); err != nil {
		return err
	}
	bundle, err := loadPostgresBundle(schemaFS, cfg)
	if err != nil {
		return err
	}
	scopedConnection, err := prepareSchemaConnection(ctx, schemaConnectionOptions{Connection: connection, Schema: cfg.schema})
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", scopedConnection)
	if err != nil {
		return fmt.Errorf("open SQL migration database: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, db.Close()) }()
	return bundle.apply(ctx, db, cfg)
}

// applyPostgresDB runs the bundle on the caller's handle. The handle cannot be
// scoped to another schema, and the script phases and metadata tables resolve
// unqualified names through its search_path, so only the default schema is
// accepted.
func applyPostgresDB(ctx context.Context, db *sql.DB, schemaFS fs.FS, cfg options) error {
	if err := refuseScriptPolicy(cfg, "PostgreSQL"); err != nil {
		return err
	}
	if err := validatePostgresBundle(schemaFS, cfg); err != nil {
		return err
	}
	if cfg.schema != defaultSchema {
		return fmt.Errorf("WithDB cannot scope search_path to schema %q on a handle it does not own: pass a connection string", cfg.schema)
	}
	bundle, err := loadPostgresBundle(schemaFS, cfg)
	if err != nil {
		return err
	}
	return bundle.apply(ctx, db, cfg)
}

// apply runs the pre scripts, the schema step, the post scripts and the
// security reconciliation, in that order.
func (b postgresBundle) apply(ctx context.Context, db *sql.DB, cfg options) error {
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect SQL migration database: %w", err)
	}
	if err := ensureMetadataTables(ctx, db); err != nil {
		return err
	}
	scope := migrationScope(cfg.schema, cfg.name)
	selected, err := selectScripts(ctx, db, scope, b.scripts)
	if err != nil {
		return err
	}
	ordered, err := topologicalScripts(b.scripts, selected)
	if err != nil {
		return err
	}
	if err := runScriptPhase(ctx, db, scriptPhaseOptions{Scope: scope, Ordered: ordered, Phase: phasePre}); err != nil {
		return err
	}
	if err := b.step.run(ctx, db); err != nil {
		return err
	}
	if err := runScriptPhase(ctx, db, scriptPhaseOptions{Scope: scope, Ordered: ordered, Phase: phasePost}); err != nil {
		return err
	}
	if err := retryOnLockContention(ctx, "reconcile database security", func() error {
		return reconcileSecurity(ctx, db, scope, b.security)
	}); err != nil {
		return fmt.Errorf("reconcile database security: %w", err)
	}
	return nil
}
