package review

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"ariga.io/atlas/sql/schema"
)

func validateScope(scope Snapshot) error {
	if scope.Schema == "" {
		return fmt.Errorf("schema scope is required")
	}
	if scope.Dialect == "sqlite" && scope.Schema != "main" {
		return fmt.Errorf("SQLite reviewed migrations require schema main")
	}
	if scope.Dialect != "sqlite" && scope.Dialect != "postgres" {
		return fmt.Errorf("unsupported reviewed schema dialect %q", scope.Dialect)
	}
	if strings.HasPrefix(scope.Schema, "pg_") || scope.Schema == "information_schema" {
		return fmt.Errorf("system schema %q cannot be migrated", scope.Schema)
	}
	return nil
}

func Inspect(ctx context.Context, db *sql.DB, dialect, schemaName string) (_ Snapshot, returnErr error) {
	if db == nil {
		return Snapshot{}, fmt.Errorf("database is nil")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, conn.Close()) }()
	b, err := open(conn, dialect)
	if err != nil {
		return Snapshot{}, err
	}
	scope := Snapshot{Dialect: dialect, Schema: schemaName}
	if err := validateScope(scope); err != nil {
		return Snapshot{}, err
	}
	s, err := inspect(ctx, conn, b, scope)
	if err != nil {
		return Snapshot{}, err
	}
	return b.encode(s, scope)
}

func inspect(ctx context.Context, q schema.ExecQuerier, b backend, scope Snapshot) (*schema.Schema, error) {
	if err := unsupported(ctx, q, scope); err != nil {
		return nil, err
	}
	s, err := b.driver.InspectSchema(ctx, scope.Schema, nil)
	if err != nil {
		return nil, fmt.Errorf("inspect schema %q: %w", scope.Schema, err)
	}
	return s, nil
}

func unsupported(ctx context.Context, q schema.ExecQuerier, scope Snapshot) (returnErr error) {
	statement := `SELECT name FROM sqlite_schema WHERE type IN ('trigger','view') OR (type = 'table' AND upper(sql) LIKE 'CREATE VIRTUAL%') LIMIT 1`
	var args []any
	if scope.Dialect == "postgres" {
		statement = `SELECT name FROM (
SELECT c.relname AS name FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname=$1 AND (c.relkind IN ('v','m','f','p') OR c.relispartition OR c.relrowsecurity OR c.relforcerowsecurity
 OR (c.relkind='S' AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid=c.oid AND d.deptype IN ('a','i'))))
UNION ALL SELECT p.proname FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=$1
UNION ALL SELECT t.tgname FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND NOT t.tgisinternal
UNION ALL SELECT c.conname FROM pg_constraint c JOIN pg_namespace n ON n.oid=c.connamespace WHERE n.nspname=$1 AND (c.contype='x' OR c.condeferrable OR NOT c.convalidated)
UNION ALL SELECT t.typname FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname=$1 AND t.typtype='d'
UNION ALL SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid=i.inhrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1
) unsupported LIMIT 1`
		args = []any{scope.Schema}
	}
	rows, err := q.QueryContext(ctx, statement, args...)
	if err != nil {
		return fmt.Errorf("inspect unsupported schema features: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	if rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		return fmt.Errorf("unsupported schema object or feature %q in %s", name, scope.Schema)
	}
	return rows.Err()
}
