package sqlserver

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// Dependents lists what depends on columns. Without a database it derives the
// answer from current, as the pure planner does. With one it reads the catalog:
// everything SQL Server refuses to alter a column under, with the definition
// that re-creates it exactly, or a Refusal for what it cannot reproduce.
func (d Dialect) Dependents(ctx context.Context, q engine.Querier, current migrate.Schema, columns []engine.ColumnRef) ([]engine.Dependent, error) {
	if q == nil {
		return d.Planner.Dependents(ctx, nil, current, columns)
	}
	var found []engine.Dependent
	for chunk := range slices.Chunk(columns, maxRequestedColumns) {
		deps, err := newReader(q, chunk).dependents(ctx)
		if err != nil {
			return nil, fmt.Errorf("sqlserver: read dependents: %w", err)
		}
		found = append(found, deps...)
	}
	return mergeDependents(found), nil
}

// reader reads the dependents of one chunk of requested columns.
type reader struct {
	q         engine.Querier
	requested []engine.ColumnRef
	cte       string
	args      []any
}

func newReader(q engine.Querier, requested []engine.ColumnRef) *reader {
	cte, args := requestedColumns(requested)
	return &reader{q: q, requested: requested, cte: cte, args: args}
}

func (r *reader) dependents(ctx context.Context) ([]engine.Dependent, error) {
	if err := r.requireColumns(ctx); err != nil {
		return nil, err
	}
	var out []engine.Dependent
	for _, read := range []func(context.Context) ([]engine.Dependent, error){
		r.indexDependents, r.foreignKeyDependents, r.checkDependents, r.statisticDependents, r.boundDependents,
	} {
		deps, err := read(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, deps...)
	}
	return out, nil
}

// requireColumns fails when a requested column is not a column of a table.
func (r *reader) requireColumns(ctx context.Context) error {
	found, err := collectRows(ctx, r.q, r.cte+resolvedColumnsQuery, r.args, func(rows *sql.Rows) (int, error) {
		var ord int
		return ord, rows.Scan(&ord)
	})
	if err != nil {
		return fmt.Errorf("resolve columns: %w", err)
	}
	for i, c := range r.requested {
		if !slices.Contains(found, i) {
			return fmt.Errorf("column %s.%s does not exist", qualified(c.Table), c.Column)
		}
	}
	return nil
}

// mergeDependents folds a dependent found through several columns into one that
// depends on all of them, in a fixed order.
func mergeDependents(deps []engine.Dependent) []engine.Dependent {
	var out []engine.Dependent
	for _, dep := range deps {
		i := slices.IndexFunc(out, func(o engine.Dependent) bool {
			return o.Kind == dep.Kind && o.Table == dep.Table && o.Name == dep.Name
		})
		if i < 0 {
			out = append(out, dep)
			continue
		}
		out[i].On = append(out[i].On, dep.On...)
	}
	for i := range out {
		slices.SortFunc(out[i].On, compareColumns)
		out[i].On = slices.Compact(out[i].On)
	}
	slices.SortFunc(out, func(a, b engine.Dependent) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), compareNames(a.Table, b.Table), cmp.Compare(a.Name, b.Name))
	})
	return out
}

func compareNames(a, b migrate.Name) int {
	return cmp.Or(cmp.Compare(a.Schema, b.Schema), cmp.Compare(a.Name, b.Name))
}

func compareColumns(a, b engine.ColumnRef) int {
	return cmp.Or(compareNames(a.Table, b.Table), cmp.Compare(a.Column, b.Column))
}
