package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/flanksource/sqlglot-go/database/migrate/dialect/atlasdriver"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

const (
	viewKind         = "view"
	materializedKind = "materialized-view"
)

// tableFeatures lists the tables that carry a feature the native snapshot
// cannot hold; the whole table is refused. A trigger is not one: PostgreSQL
// keeps it across every ALTER TABLE except a retype or drop of a column it
// reads, which Dependents refuses.
const tableFeatures = `
SELECT c.relname, 'partitioned or foreign table' FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = $1 AND c.relkind IN ('p','f')
UNION ALL SELECT c.relname, 'partition' FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = $1 AND c.relispartition
UNION ALL SELECT c.relname, 'row level security' FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = $1 AND c.relkind = 'r' AND (c.relrowsecurity OR c.relforcerowsecurity)
UNION ALL SELECT c.relname, 'table inheritance' FROM pg_inherits i JOIN pg_class c ON c.oid IN (i.inhrelid, i.inhparent)
 JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1`

// constraintFeatures lists the constraints whose kind or property the native
// snapshot cannot hold. Each is refused as a member of its table: a script can
// own the property (ALTER CONSTRAINT ... DEFERRABLE), and PostgreSQL keeps it
// across every ALTER TABLE that leaves the constraint alone, so only a change
// to the constraint itself is refused.
const constraintFeatures = `
SELECT c.relname, k.conname FROM pg_constraint k JOIN pg_class c ON c.oid = k.conrelid JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = $1 AND (k.contype = 'x' OR k.condeferrable OR NOT k.convalidated)`

// otherObjects lists objects of the schema the dialect does not manage; the
// second column names the kind, which becomes the refusal's member so a
// refusal of a function never matches a table of the same name.
const otherObjects = `
SELECT c.relname, 'sequence' FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = $1 AND c.relkind = 'S'
   AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = c.oid AND d.deptype IN ('a','i'))
UNION ALL SELECT DISTINCT p.proname, 'function' FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
 WHERE n.nspname = $1 AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = p.oid AND d.deptype = 'e')
UNION ALL SELECT t.typname, 'domain' FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
 WHERE n.nspname = $1 AND t.typtype = 'd'
UNION ALL SELECT t.typname, 'type' FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
 WHERE n.nspname = $1 AND ((t.typtype = 'c' AND EXISTS (SELECT 1 FROM pg_class c WHERE c.oid = t.typrelid AND c.relkind = 'c')) OR t.typtype = 'r')`

const viewDefinitions = `
SELECT c.relname, c.relkind::text, pg_get_viewdef(c.oid, true) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = $1 AND c.relkind IN ('v','m') ORDER BY c.relname`

// Extras reports what Atlas does not model: tables carrying features the
// snapshot cannot hold (refused whole), other schema objects (refused by
// name) and views, which are carried as definitions so a forced rebuild can
// drop and re-create them.
func (backend) Extras(ctx context.Context, q engine.Querier, schemaName string) (atlasdriver.Extras, error) {
	var extras atlasdriver.Extras
	tables, err := queryPairs(ctx, q, tableFeatures, schemaName)
	if err != nil {
		return extras, fmt.Errorf("inspect table features: %w", err)
	}
	for _, t := range tables {
		extras.Refusals = append(extras.Refusals, engine.Refusal{Object: migrate.Name{Schema: schemaName, Name: t[0]}, Reason: t[1]})
	}
	constraints, err := queryPairs(ctx, q, constraintFeatures, schemaName)
	if err != nil {
		return extras, fmt.Errorf("inspect constraint features: %w", err)
	}
	for _, k := range constraints {
		extras.Refusals = append(extras.Refusals, engine.Refusal{
			Object: migrate.Name{Schema: schemaName, Name: k[0]}, Member: k[1],
			Reason: fmt.Sprintf("constraint %s is an exclusion, deferrable or not validated constraint", k[1]),
		})
	}
	others, err := queryPairs(ctx, q, otherObjects, schemaName)
	if err != nil {
		return extras, fmt.Errorf("inspect other objects: %w", err)
	}
	for _, o := range others {
		extras.Refusals = append(extras.Refusals, engine.Refusal{
			Object: migrate.Name{Schema: schemaName, Name: o[0]}, Member: o[1], Reason: fmt.Sprintf("%s objects are not managed", o[1]),
		})
	}
	extras.Definitions, err = liveViews(ctx, q, schemaName)
	return extras, err
}

func liveViews(ctx context.Context, q engine.Querier, schemaName string) (_ []migrate.Definition, returnErr error) {
	rows, err := q.QueryContext(ctx, viewDefinitions, schemaName)
	if err != nil {
		return nil, fmt.Errorf("inspect views: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	var out []migrate.Definition
	for rows.Next() {
		var name, relkind, body string
		if err := rows.Scan(&name, &relkind, &body); err != nil {
			return nil, err
		}
		kind := viewKind
		if relkind == "m" {
			kind = materializedKind
		}
		out = append(out, migrate.Definition{Name: migrate.Name{Schema: schemaName, Name: name}, Kind: kind, SQL: body})
	}
	return out, rows.Err()
}

func queryPairs(ctx context.Context, q engine.Querier, query string, args ...any) (_ [][2]string, returnErr error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	var out [][2]string
	for rows.Next() {
		var a, b sql.NullString
		if err := rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		out = append(out, [2]string{a.String, b.String})
	}
	return out, rows.Err()
}
