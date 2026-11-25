package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/database/migrate/dialect/atlasdriver"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/changeset"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// Forcing marks the changes PostgreSQL refuses while a view reads what they
// change. A column type change forces the views to be dropped and re-created
// around it. A column or table drop forces nothing to be re-created, since the
// views cannot exist without it: it is marked so Dependents finds those views
// and the plan can refuse them before anything runs.
func (backend) Forcing(set *changeset.Set, changes []schema.Change) ([]engine.Forcing, error) {
	var out []engine.Forcing
	for _, c := range changes {
		var found []engine.Forcing
		var err error
		switch c := c.(type) {
		case *schema.ModifyTable:
			found, err = columnForcings(set, c)
		case *schema.DropTable:
			found, err = tableForcing(set, c)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	return out, nil
}

func columnForcings(set *changeset.Set, m *schema.ModifyTable) ([]engine.Forcing, error) {
	var out []engine.Forcing
	for _, child := range m.Changes {
		var column string
		var dropped engine.DroppedKind
		switch child := child.(type) {
		case *schema.ModifyColumn:
			if !child.Change.Is(schema.ChangeType) {
				continue
			}
			column = child.To.Name
		case *schema.DropColumn:
			column, dropped = child.C.Name, engine.DroppedColumn
		default:
			continue
		}
		if m.T.Schema == nil {
			return nil, fmt.Errorf("table %q has no schema", m.T.Name)
		}
		ids, err := set.Lookup(child)
		if err != nil {
			return nil, err
		}
		ref := engine.ColumnRef{Table: migrate.Name{Schema: m.T.Schema.Name, Name: m.T.Name}, Column: column, Dropped: dropped}
		out = append(out, engine.Forcing{Change: ids[0], Columns: []engine.ColumnRef{ref}})
	}
	return out, nil
}

// tableForcing marks a table drop by every column of the table and by the table
// itself, which a view that reads no column (count(*)) depends on.
func tableForcing(set *changeset.Set, drop *schema.DropTable) ([]engine.Forcing, error) {
	if drop.T.Schema == nil {
		return nil, fmt.Errorf("table %q has no schema", drop.T.Name)
	}
	ids, err := set.Lookup(drop)
	if err != nil {
		return nil, err
	}
	table := migrate.Name{Schema: drop.T.Schema.Name, Name: drop.T.Name}
	refs := []engine.ColumnRef{{Table: table, Dropped: engine.DroppedTable}}
	for _, col := range drop.T.Columns {
		refs = append(refs, engine.ColumnRef{Table: table, Column: col.Name, Dropped: engine.DroppedTable})
	}
	return []engine.Forcing{{Change: ids[0], Columns: refs}}, nil
}

// dependentViews returns every view and materialized view that transitively
// reads one of the given columns: the first level through the column's own
// pg_depend rows, the views built on those through any dependency. Rows come
// back with the targeted column's index so each view records what it depends on.
const dependentViews = `
WITH RECURSIVE targets(idx, rel, col) AS (VALUES %s),
roots AS (
  SELECT t.idx, c.oid AS reloid, a.attnum
  FROM targets t
  JOIN pg_class c ON c.oid = to_regclass(t.rel)
  JOIN pg_attribute a ON a.attrelid = c.oid AND a.attname = t.col AND NOT a.attisdropped
  UNION ALL
  SELECT t.idx, c.oid, 0
  FROM targets t
  JOIN pg_class c ON c.oid = to_regclass(t.rel)
  WHERE t.col = ''
),
deps(view_oid, depth, idx) AS (
  SELECT r.ev_class, 1, roots.idx
  FROM roots
  JOIN pg_depend d ON d.refclassid = 'pg_class'::regclass AND d.refobjid = roots.reloid AND d.refobjsubid = roots.attnum
   AND d.classid = 'pg_rewrite'::regclass
  JOIN pg_rewrite r ON r.oid = d.objid
  WHERE r.ev_class <> roots.reloid
  UNION ALL
  SELECT r.ev_class, deps.depth + 1, deps.idx
  FROM deps
  JOIN pg_depend d ON d.refclassid = 'pg_class'::regclass AND d.refobjid = deps.view_oid AND d.classid = 'pg_rewrite'::regclass
  JOIN pg_rewrite r ON r.oid = d.objid
  WHERE r.ev_class <> d.refobjid
)
SELECT n.nspname, c.relname, c.relkind::text, deps.idx, MAX(deps.depth)
FROM deps
JOIN pg_class c ON c.oid = deps.view_oid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('v', 'm')
GROUP BY 1, 2, 3, 4
ORDER BY MAX(deps.depth), 1, 2, 4`

type viewRef struct {
	schema, name, kind string
}

// Dependents lists the views that read the forcing columns, with the
// statements that re-create each. A view that reads a dropped column or table
// directly carries a Refusal instead: it cannot be re-created once what it reads
// is gone, so the plan fails before anything runs and the reason holds the
// definition to save. Offline there are none: the catalog is the only place a
// view's dependencies are recorded.
func (backend) Dependents(ctx context.Context, q engine.Querier, _ migrate.Schema, columns []engine.ColumnRef) ([]engine.Dependent, error) {
	if q == nil || len(columns) == 0 {
		return nil, nil
	}
	found, err := findViews(ctx, q, columns)
	if err != nil {
		return nil, err
	}
	out := make([]engine.Dependent, 0, len(found.refs))
	for _, v := range found.refs {
		stmts, err := captureView(ctx, q, v)
		if err != nil {
			return nil, err
		}
		table := migrate.Name{Schema: v.schema, Name: v.name}
		definition := strings.Join(stmts, ";\n")
		dep := engine.Dependent{Kind: engine.DependentView, Table: table, Name: v.name, On: found.on[table], Definition: definition}
		if read, ok := found.dropped[table]; ok {
			dep.Refusal = droppedRefusal(table, read, definition)
		}
		out = append(out, dep)
	}
	return out, nil
}

// droppedRefusal names the view, what it reads that is dropped, and the
// statements that re-create it as it is now.
func droppedRefusal(view migrate.Name, read engine.ColumnRef, definition string) string {
	table := read.Table.Schema + "." + read.Table.Name
	what := "table " + table
	switch {
	case read.Dropped == engine.DroppedColumn:
		what = fmt.Sprintf("column %s of table %s", read.Column, table)
	case read.Column != "":
		what = fmt.Sprintf("column %s of table %s, which is dropped with it", read.Column, table)
	}
	return fmt.Sprintf("view %s.%s reads %s, which the plan drops, so the view cannot be re-created afterwards; save its definition and drop the view first: %s",
		view.Schema, view.Name, what, definition)
}

type foundViews struct {
	refs []viewRef
	// on lists the forcing columns each view depends on, as the engine passed them.
	on map[migrate.Name][]engine.ColumnRef
	// dropped names a dropped column or table each view reads directly.
	dropped map[migrate.Name]engine.ColumnRef
}

func findViews(ctx context.Context, q engine.Querier, columns []engine.ColumnRef) (_ foundViews, returnErr error) {
	values := make([]string, len(columns))
	args := make([]any, 0, 3*len(columns))
	for i, c := range columns {
		values[i] = fmt.Sprintf("($%d::int, $%d::text, $%d::text)", 3*i+1, 3*i+2, 3*i+3)
		args = append(args, i, atlasdriver.QuoteIdent(c.Table.Schema)+"."+atlasdriver.QuoteIdent(c.Table.Name), c.Column)
	}
	rows, err := q.QueryContext(ctx, fmt.Sprintf(dependentViews, strings.Join(values, ", ")), args...)
	if err != nil {
		return foundViews{}, fmt.Errorf("inspect view dependencies: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	found := foundViews{on: map[migrate.Name][]engine.ColumnRef{}, dropped: map[migrate.Name]engine.ColumnRef{}}
	for rows.Next() {
		var v viewRef
		var idx, depth int
		if err := rows.Scan(&v.schema, &v.name, &v.kind, &idx, &depth); err != nil {
			return foundViews{}, err
		}
		name := migrate.Name{Schema: v.schema, Name: v.name}
		if _, seen := found.on[name]; !seen {
			found.refs = append(found.refs, v)
		}
		found.on[name] = append(found.on[name], columns[idx])
		if columns[idx].Dropped != "" && depth == 1 {
			if _, seen := found.dropped[name]; !seen {
				found.dropped[name] = columns[idx]
			}
		}
	}
	return found, rows.Err()
}

const (
	captureViewSQL = `SELECT pg_get_viewdef(c.oid, true), pg_get_userbyid(c.relowner), COALESCE(obj_description(c.oid, 'pg_class'), ''),
 COALESCE(array_to_string(c.reloptions, ','), '')
 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relname = $2`
	viewColumnsSQL = `SELECT a.attname FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = $1 AND c.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum`
	captureIndexesSQL = `SELECT indexdef FROM pg_indexes WHERE schemaname = $1 AND tablename = $2 ORDER BY indexname`
	captureGrantsSQL  = `SELECT CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(a.grantee) END, a.privilege_type, a.is_grantable
 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace CROSS JOIN LATERAL aclexplode(c.relacl) a
 WHERE n.nspname = $1 AND c.relname = $2 ORDER BY 1, 2`
)

// captureView reads everything needed to re-create a view as it was: its
// body, then owner, options, indexes (materialized views), comment and grants.
func captureView(ctx context.Context, q engine.Querier, v viewRef) ([]string, error) {
	var body, owner, comment, options string
	rows, err := q.QueryContext(ctx, captureViewSQL, v.schema, v.name)
	if err != nil {
		return nil, fmt.Errorf("capture view %s.%s: %w", v.schema, v.name, err)
	}
	if rows.Next() {
		err = rows.Scan(&body, &owner, &comment, &options)
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return nil, fmt.Errorf("capture view %s.%s: %w", v.schema, v.name, err)
	}
	if body == "" {
		return nil, fmt.Errorf("capture view %s.%s: not found", v.schema, v.name)
	}
	columns, err := queryStrings(ctx, q, viewColumnsSQL, v.schema, v.name)
	if err != nil {
		return nil, fmt.Errorf("capture columns of %s.%s: %w", v.schema, v.name, err)
	}
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = atlasdriver.QuoteIdent(c)
	}
	name := atlasdriver.NameString(migrate.Name{Schema: v.schema, Name: v.name})
	keyword, suffix := "VIEW", ""
	if v.kind == "m" {
		keyword, suffix = "MATERIALIZED VIEW", " WITH DATA"
	}
	create := fmt.Sprintf("CREATE %s %s (%s)%s%s%s", keyword, name, strings.Join(quoted, ", "), bodyMarker, strings.TrimSuffix(strings.TrimSpace(body), ";"), suffix)
	stmts := []string{create, fmt.Sprintf("ALTER %s %s OWNER TO %s", keyword, name, atlasdriver.QuoteIdent(owner))}
	if options != "" {
		stmts = append(stmts, fmt.Sprintf("ALTER %s %s SET (%s)", keyword, name, options))
	}
	if v.kind == "m" {
		indexes, err := queryStrings(ctx, q, captureIndexesSQL, v.schema, v.name)
		if err != nil {
			return nil, fmt.Errorf("capture indexes of %s.%s: %w", v.schema, v.name, err)
		}
		stmts = append(stmts, indexes...)
	}
	if comment != "" {
		stmts = append(stmts, fmt.Sprintf("COMMENT ON %s %s IS '%s'", keyword, name, strings.ReplaceAll(comment, "'", "''")))
	}
	grants, err := captureGrants(ctx, q, v, name)
	return append(stmts, grants...), err
}

func captureGrants(ctx context.Context, q engine.Querier, v viewRef, name string) (_ []string, returnErr error) {
	rows, err := q.QueryContext(ctx, captureGrantsSQL, v.schema, v.name)
	if err != nil {
		return nil, fmt.Errorf("capture grants of %s.%s: %w", v.schema, v.name, err)
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	var out []string
	for rows.Next() {
		var grantee, privilege string
		var grantable bool
		if err := rows.Scan(&grantee, &privilege, &grantable); err != nil {
			return nil, err
		}
		target := atlasdriver.QuoteIdent(grantee)
		if grantee == "PUBLIC" {
			target = grantee
		}
		stmt := fmt.Sprintf("GRANT %s ON %s TO %s", privilege, name, target)
		if grantable {
			stmt += " WITH GRANT OPTION"
		}
		out = append(out, stmt)
	}
	return out, rows.Err()
}

func queryStrings(ctx context.Context, q engine.Querier, query string, args ...any) (_ []string, returnErr error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
