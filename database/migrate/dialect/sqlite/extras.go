package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/database/migrate/dialect/atlasdriver"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/changeset"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

const viewKind = "view"

// Extras reports what Atlas does not model. Virtual tables cannot be copied,
// so they refuse the table; views are carried as definitions so a rebuild can
// drop and re-create them. Triggers are lost when their table is rebuilt, but
// only then: Dependents refuses a rebuild that would discard one.
func (backend) Extras(ctx context.Context, q engine.Querier, _ string) (atlasdriver.Extras, error) {
	var extras atlasdriver.Extras
	virtual, err := queryPairs(ctx, q, `SELECT name, name FROM sqlite_schema WHERE type = 'table' AND upper(sql) LIKE 'CREATE VIRTUAL%'`)
	if err != nil {
		return extras, err
	}
	for _, t := range virtual {
		extras.Refusals = append(extras.Refusals, engine.Refusal{Object: migrate.Name{Schema: SchemaName, Name: t[0]}, Reason: "virtual tables are not supported"})
	}
	views, err := liveViews(ctx, q)
	if err != nil {
		return extras, err
	}
	extras.Definitions = views
	return extras, nil
}

func liveViews(ctx context.Context, q engine.Querier) ([]migrate.Definition, error) {
	rows, err := queryPairs(ctx, q, `SELECT name, sql FROM sqlite_schema WHERE type = 'view' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	out := make([]migrate.Definition, len(rows))
	for i, r := range rows {
		out[i] = migrate.Definition{Name: migrate.Name{Schema: SchemaName, Name: r[0]}, Kind: viewKind, SQL: r[1]}
	}
	return out, nil
}

func queryPairs(ctx context.Context, q engine.Querier, query string) (_ [][2]string, returnErr error) {
	rows, err := q.QueryContext(ctx, query)
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

// Forcing marks every change of a table that must be rebuilt: the rebuild
// drops the table, so views over it are dropped and re-created around it.
func (backend) Forcing(set *changeset.Set, changes []schema.Change) ([]engine.Forcing, error) {
	var out []engine.Forcing
	for _, c := range changes {
		m, ok := c.(*schema.ModifyTable)
		if !ok || !slices.ContainsFunc(m.Changes, rebuilds) {
			continue
		}
		ids, err := set.Lookup(m)
		if err != nil {
			return nil, err
		}
		var columns []engine.ColumnRef
		for _, col := range m.T.Columns {
			columns = append(columns, engine.ColumnRef{Table: migrate.Name{Schema: SchemaName, Name: m.T.Name}, Column: col.Name})
		}
		for _, id := range ids {
			out = append(out, engine.Forcing{Change: id, Columns: columns})
		}
	}
	return out, nil
}

// Dependents lists the views built on the forcing columns' tables, and the
// views built on those, read from the database or, offline, from current. Read
// from the database it also lists the triggers on those tables, each carrying
// the refusal that a rebuild would discard it.
func (backend) Dependents(ctx context.Context, q engine.Querier, current migrate.Schema, columns []engine.ColumnRef) ([]engine.Dependent, error) {
	var views []migrate.Definition
	if q == nil {
		for _, d := range current.Definitions {
			if d.Kind == viewKind {
				views = append(views, d)
			}
		}
	} else {
		var err error
		if views, err = liveViews(ctx, q); err != nil {
			return nil, err
		}
	}
	byTable := map[migrate.Name][]engine.ColumnRef{}
	for _, c := range columns {
		byTable[c.Table] = append(byTable[c.Table], c)
	}
	var out []engine.Dependent
	for table, cols := range byTable {
		for _, v := range viewsOver(views, table.Name) {
			out = append(out, engine.Dependent{Kind: engine.DependentView, Table: v.Name, Name: v.Name.Name, On: cols, Definition: v.SQL})
		}
	}
	if q == nil {
		return out, nil
	}
	triggers, err := triggerDependents(ctx, q, byTable)
	return append(out, triggers...), err
}

func triggerDependents(ctx context.Context, q engine.Querier, byTable map[migrate.Name][]engine.ColumnRef) ([]engine.Dependent, error) {
	rows, err := queryPairs(ctx, q, `SELECT name, tbl_name FROM sqlite_schema WHERE type = 'trigger' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("inspect triggers: %w", err)
	}
	var out []engine.Dependent
	for _, r := range rows {
		table := migrate.Name{Schema: SchemaName, Name: r[1]}
		if cols, ok := byTable[table]; ok {
			out = append(out, engine.Dependent{
				Kind: engine.DependentTable, Table: table, Name: r[0], On: cols,
				Refusal: fmt.Sprintf("trigger %q would be lost when the table is rebuilt", r[0]),
			})
		}
	}
	return out, nil
}

// viewsOver returns the views that mention name, then the views that mention those.
func viewsOver(views []migrate.Definition, name string) []migrate.Definition {
	var found []migrate.Definition
	seen := map[string]bool{}
	frontier := []string{name}
	for len(frontier) > 0 {
		var next []string
		for _, v := range views {
			if seen[v.Name.Name] {
				continue
			}
			if slices.ContainsFunc(frontier, func(n string) bool { return atlasdriver.Mentions(v.SQL, n) }) {
				seen[v.Name.Name] = true
				found = append(found, v)
				next = append(next, v.Name.Name)
			}
		}
		frontier = next
	}
	return found
}
