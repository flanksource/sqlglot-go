package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/commons/logger"
	"github.com/flanksource/sqlglot-go/database/atlas/sqlserver"
	backend "github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver"
)

const (
	// sqlServerSchema is the only schema SQL Server migrations manage.
	sqlServerSchema = "dbo"
)

// applySQLServer plans the bundle and applies that plan: its pre scripts, the
// reconciliation of its HCL tables and WithRoutines routines against the dbo
// schema, then its post scripts. It inspects only the objects the bundle
// names, never drops an object the bundle does not declare, never drops a
// column (each column a declared table lacks is a logged warning), and runs no
// DDL when the database already matches. Scripts are recorded in
// dbo.sqlglot_migrations. The caller owns db.
func applySQLServer(ctx context.Context, db *sql.DB, schemaFS fs.FS, cfg options) error {
	file, warnings, err := planSQLServer(ctx, db, schemaFS, cfg)
	if err != nil {
		return err
	}
	log := logger.GetLogger("migrate")
	for _, warning := range warnings {
		log.Warnf("%s", warning)
	}
	result, err := backend.Apply(ctx, db, file)
	if err != nil {
		return err
	}
	log.V(1).Infof("SQL Server migration %s: %d scripts applied", result.Outcome, len(result.Applied))
	return nil
}

// planSQLServer is the read-only plan file of the bundle on db, and the
// warnings for the undeclared columns its schema step leaves in place.
func planSQLServer(ctx context.Context, db *sql.DB, schemaFS fs.FS, cfg options) (backend.PlanFile, []string, error) {
	bundle, err := loadSQLServerBundle(schemaFS, cfg)
	if err != nil {
		return backend.PlanFile{}, nil, err
	}
	options, err := scriptOptions(db, bundle, cfg)
	if err != nil {
		return backend.PlanFile{}, nil, err
	}
	var warnings []string
	if bundle.declaresObjects() {
		selected, err := planSchemaStep(ctx, db, bundle, cfg)
		if err != nil {
			return backend.PlanFile{}, nil, err
		}
		options.Schema, warnings = selected.Artifact, selected.Warnings
	}
	file, err := backend.Plan(ctx, options)
	return file, warnings, err
}

// sqlServerDesired builds the desired dbo schema from the HCL tables and the
// routine files, refusing what SQL Server migrations do not support. The
// bundle's scripts are loaded by loadSQLServerBundle.
func sqlServerDesired(schemaFS fs.FS, cfg options) (*schema.Realm, error) {
	if schemaFS == nil {
		return nil, errors.New("schema filesystem is nil")
	}
	if err := refuseSQLServerOptions(cfg); err != nil {
		return nil, err
	}
	realm, err := sqlServerTables(schemaFS, cfg)
	if err != nil {
		return nil, err
	}
	if cfg.routines != "" {
		if err := addRoutines(schemaFS, cfg.routines, realm.Schemas[0]); err != nil {
			return nil, err
		}
	}
	return realm, nil
}

func refuseSQLServerOptions(cfg options) error {
	switch {
	case cfg.schemaSet && cfg.schema != sqlServerSchema:
		return fmt.Errorf("SQL Server migrations support only schema %q, got %q", sqlServerSchema, cfg.schema)
	case cfg.allowDrops:
		return errors.New("SQL Server migrations do not support destructive changes (WithDrops)")
	case len(cfg.exclude) > 0:
		return errors.New("SQL Server migrations do not support WithExclude: only the declared objects are inspected")
	}
	return nil
}

func addRoutines(schemaFS fs.FS, dir string, target *schema.Schema) error {
	routines, err := loadRoutines(schemaFS, dir)
	if err != nil {
		return err
	}
	for _, routine := range routines {
		if _, exists := target.Table(routine.Name); exists {
			return fmt.Errorf("routine %s has the name of a declared table", routine.Name)
		}
		if err := routine.AddTo(target); err != nil {
			return err
		}
	}
	return nil
}

// sqlServerTables evaluates the bundle's HCL, if it has any, into a realm
// holding exactly the dbo schema.
func sqlServerTables(schemaFS fs.FS, cfg options) (*schema.Realm, error) {
	found, err := hasHCL(schemaFS, cfg.dir)
	if err != nil {
		return nil, err
	}
	if !found {
		return schema.NewRealm(schema.New(sqlServerSchema)), nil
	}
	loaded, err := loadHCL(hclSource{FS: schemaFS, Dir: cfg.dir, Input: cfg.input, PartialInSource: true})
	if err != nil {
		return nil, err
	}
	if len(loaded.Security.Roles) > 0 || len(loaded.Security.Permissions) > 0 {
		return nil, errors.New("SQL Server migrations do not support roles or permissions")
	}
	realm := &schema.Realm{}
	if err := sqlserver.EvalHCL.Eval(loaded.Parser, realm, cfg.input); err != nil {
		return nil, fmt.Errorf("evaluate HCL schemas for SQL Server: %w", err)
	}
	if len(realm.Schemas) != 1 || realm.Schemas[0].Name != sqlServerSchema {
		return nil, fmt.Errorf("SQL Server HCL must declare exactly schema %q, got %s", sqlServerSchema, schemaNames(realm))
	}
	return realm, nil
}

func hasHCL(schemaFS fs.FS, dir string) (bool, error) {
	found := false
	err := fs.WalkDir(schemaFS, dir, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && strings.EqualFold(path.Ext(name), ".hcl") {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("find HCL schemas in %q: %w", dir, err)
	}
	return found, nil
}

func schemaNames(realm *schema.Realm) string {
	names := make([]string, len(realm.Schemas))
	for i, s := range realm.Schemas {
		names[i] = fmt.Sprintf("%q", s.Name)
	}
	return "[" + strings.Join(names, ", ") + "]"
}

func scriptPaths(scripts map[string]*script) []string {
	paths := make([]string, 0, len(scripts))
	for p := range scripts {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	return paths
}
