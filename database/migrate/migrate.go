// Package migrate applies declarative Atlas HCL schemas to PostgreSQL, SQLite
// and SQL Server databases, and on SQL Server also the stored procedures and
// functions declared as .sql files (WithRoutines).
package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	"github.com/flanksource/sqlglot-go/database/target"
	"github.com/lib/pq"
	mssql "github.com/microsoft/go-mssqldb"
	"github.com/zclconf/go-cty/cty"
	"modernc.org/sqlite"
)

type options struct {
	dir           string
	name          string
	schema        string
	schemaSet     bool
	routines      string
	exclude       []string
	allowDrops    bool
	allowRebuilds bool
	input         map[string]cty.Value
	db            *sql.DB
	scriptPolicy  *ScriptPolicy
	scriptSources map[string]scripts.Source
}

// Option configures an HCL migration.
type Option func(*options)

// WithDir selects the root containing colocated HCL and SQL migrations.
func WithDir(dir string) Option {
	return func(o *options) { o.dir = strings.Trim(path.Clean(dir), "/") }
}

// WithName sets the metadata scope used for SQL hashes and managed security.
// It should be stable and unique for each migration bundle sharing a database.
func WithName(name string) Option {
	return func(o *options) { o.name = strings.TrimSpace(name) }
}

// WithSchema selects the PostgreSQL schema used for the complete migration lifecycle.
// SQLite accepts only the default public schema declared by portable HCL bundles,
// and SQL Server only dbo.
func WithSchema(name string) Option {
	return func(o *options) { o.schema, o.schemaSet = name, true }
}

// WithVariables supplies values for HCL variable blocks and security expressions.
func WithVariables(input map[string]cty.Value) Option {
	return func(o *options) { o.input = input }
}

// WithExclude hides database objects from the plan: they are removed from the
// live schema and from the declared one before they are compared, so they are
// neither created, altered nor dropped. Values use Atlas's schema inspection
// patterns (for example "table" or "table.column"); SQLite accepts whole
// tables only, because it rebuilds a table from what the plan can see.
func WithExclude(patterns ...string) Option {
	return func(o *options) { o.exclude = append(o.exclude, patterns...) }
}

// WithDrops makes the bundle the complete description of its schema: the plan
// is planned in the engine's complete scope, so anything the HCL does not
// declare (tables, types, columns, indexes, keys, checks) is dropped. Without
// it the scope is declared: only what the HCL names is managed, and nothing
// undeclared is ever dropped, so a partial bundle cannot delete objects owned by
// consumers. The bundle's own metadata tables are never dropped.
func WithDrops() Option {
	return func(o *options) { o.allowDrops = true }
}

// WithRebuilds retains source compatibility. Supported SQLite rebuilds are now automatic.
func WithRebuilds() Option {
	return func(o *options) { o.allowRebuilds = true }
}

// WithDB runs the migration on an already open SQL Server, SQLite or
// PostgreSQL handle, with Apply's connection string left empty; the dialect
// comes from the handle's driver. Apply never closes db and never changes its
// pool settings. Every schema unit runs on a dedicated connection that sets and
// restores its own lock timeout, so a PostgreSQL handle needs no scoping; what
// it cannot do is move the script phases and metadata tables to another schema,
// so a PostgreSQL handle is accepted for the default schema only
// (WithSchema needs a connection string, which scopes search_path).
func WithDB(db *sql.DB) Option {
	return func(o *options) { o.db = db }
}

// Apply resolves the database target from connection, or from the WithDB
// handle, and reconciles HCL schemas.
//
// Apply is the unreviewed path: it plans the schema against the live database
// with the shared planner (engine.PlanLive) and applies the plan in the same
// call, acknowledging every destructive change of its own plan
// (engine.Acknowledgements). Nothing but the scope bounds what it may destroy:
// declared by default, where only the objects the HCL names are managed and
// nothing undeclared is dropped, and complete with WithDrops. Use the reviewed
// workflow when a person must approve the changes first.
//
// PostgreSQL supports the complete HCL, SQL, security, and destructive-change
// lifecycle; the views that depend on a retyped column are dropped and
// re-created around the change from their live definitions. SQLite projects
// portable PostgreSQL-shaped HCL to tables, columns, and indexes, rebuilding
// tables when needed and keeping undeclared columns and tables unless
// WithDrops is enabled.
// SQL Server (sqlserver:// URLs, ADO and odbc: strings) reconciles SQL Server HCL tables and the
// stored procedures and functions declared with WithRoutines, inspecting only
// the objects the bundle names; it refuses SQL scripts, security blocks,
// drops, rebuilds and schemas other than dbo.
func Apply(ctx context.Context, connection string, schemaFS fs.FS, opts ...Option) (returnErr error) {
	config := resolveOptions(opts)
	if config.db != nil {
		if strings.TrimSpace(connection) != "" {
			return errors.New("pass a connection string or WithDB, not both")
		}
		return applyDB(ctx, config.db, schemaFS, config)
	}
	if strings.TrimSpace(connection) == "" {
		return errors.New("connection string is empty")
	}
	target, err := dbtarget.Parse(connection)
	if err != nil {
		return err
	}
	switch target.Dialect {
	case dbtarget.SQLite:
		db, err := openSQLite(target.DSN)
		if err != nil {
			return err
		}
		defer func() { returnErr = errors.Join(returnErr, db.Close()) }()
		return applySQLite(ctx, db, schemaFS, config)
	case dbtarget.SQLServer:
		db, err := sql.Open("sqlserver", target.DSN)
		if err != nil {
			return fmt.Errorf("open SQL Server migration database: %w", err)
		}
		defer func() { returnErr = errors.Join(returnErr, db.Close()) }()
		return applySQLServer(ctx, db, schemaFS, config)
	case dbtarget.Postgres:
		return applyPostgres(ctx, target.DSN, schemaFS, config)
	default:
		return fmt.Errorf("unsupported migration dialect %q", target.Dialect)
	}
}

// applyDB dispatches a WithDB handle on its driver.
func applyDB(ctx context.Context, db *sql.DB, schemaFS fs.FS, config options) error {
	switch driver := db.Driver().(type) {
	case *mssql.Driver:
		return applySQLServer(ctx, db, schemaFS, config)
	case *sqlite.Driver:
		return applySQLite(ctx, db, schemaFS, config)
	case *pq.Driver:
		return applyPostgresDB(ctx, db, schemaFS, config)
	default:
		return fmt.Errorf("WithDB does not support database driver %T; use a SQL Server, SQLite or PostgreSQL (lib/pq) handle", driver)
	}
}

func resolveOptions(opts []Option) options {
	cfg := options{dir: ".", schema: defaultSchema}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.dir == "" {
		cfg.dir = "."
	}
	if cfg.name == "" {
		cfg.name = cfg.dir
		if cfg.name == "." {
			cfg.name = "default"
		}
	}
	cfg.exclude = append([]string(nil), cfg.exclude...)
	cfg.input = cloneVariables(cfg.input)
	return cfg
}

func cloneVariables(input map[string]cty.Value) map[string]cty.Value {
	if input == nil {
		return nil
	}
	cloned := make(map[string]cty.Value, len(input))
	for name, value := range input {
		cloned[name] = value
	}
	return cloned
}
