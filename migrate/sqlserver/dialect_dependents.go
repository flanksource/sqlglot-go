package sqlserver

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate"
	atlas "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	"github.com/flanksource/sqlglot-go/sqlglot"
)

// Dependents derives, from current alone, every index, key, foreign key and
// check that depends on the columns. It over-includes: an expression that
// names a column as a token is taken to depend on it. A column's own default
// is not listed: the column's change drops and re-binds it itself.
func (Planner) Dependents(_ context.Context, q engine.Querier, current migrate.Schema, columns []engine.ColumnRef) ([]engine.Dependent, error) {
	if q != nil {
		return nil, needsDatabase("reading live dependents")
	}
	var found []engine.Dependent
	for _, c := range columns {
		table, ok := findTable(current, c.Table)
		if !ok || !slices.ContainsFunc(table.Columns, func(col migrate.Column) bool { return col.Name == c.Column }) {
			return nil, fmt.Errorf("sqlserver: dependents of %s.%s: no such column in the current schema", qualified(c.Table), c.Column)
		}
		deps, err := dependentsOf(current, table, c)
		if err != nil {
			return nil, err
		}
		found = append(found, deps...)
	}
	return mergeDependents(found), nil
}

func dependentsOf(current migrate.Schema, table migrate.Table, c engine.ColumnRef) ([]engine.Dependent, error) {
	var out []engine.Dependent
	for _, idx := range table.Indexes {
		uses, err := indexUses(idx, c.Column)
		if err != nil {
			return nil, err
		}
		if uses {
			out = append(out, indexDependent(table.Name, idx, c))
		}
	}
	for _, t := range current.Tables {
		for _, fk := range t.ForeignKeys {
			if foreignKeyUses(t.Name, fk, c) {
				out = append(out, engine.Dependent{Kind: engine.DependentForeignKey, Table: t.Name, Name: fk.Name, On: []engine.ColumnRef{c}, ForeignKey: new(cloneForeignKey(fk))})
			}
		}
	}
	for _, check := range table.Checks {
		uses, err := mentions(check.Expression, c.Column)
		if err != nil {
			return nil, fmt.Errorf("sqlserver: check %s on %s: %w", check.Name, qualified(table.Name), err)
		}
		if uses {
			out = append(out, engine.Dependent{Kind: engine.DependentCheck, Table: table.Name, Name: check.Name, On: []engine.ColumnRef{c}, Check: new(check)})
		}
	}
	return out, nil
}

func hasName(names []string, name string) bool {
	return slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, name) })
}

func indexUses(idx migrate.Index, column string) (bool, error) {
	if slices.ContainsFunc(idx.Columns, func(c migrate.IndexColumn) bool { return strings.EqualFold(c.Name, column) }) || hasName(idx.Included, column) {
		return true, nil
	}
	if idx.Filter == "" {
		return false, nil
	}
	uses, err := mentions(idx.Filter, column)
	if err != nil {
		return false, fmt.Errorf("sqlserver: index %s filter: %w", idx.Name, err)
	}
	return uses, nil
}

// foreignKeyUses reports a key with the column on its child or parent side.
func foreignKeyUses(owner migrate.Name, fk migrate.ForeignKey, c engine.ColumnRef) bool {
	return owner == c.Table && hasName(fk.Columns, c.Column) || fk.References == c.Table && hasName(fk.ReferencedColumns, c.Column)
}

// indexDependent uses the kind its change carries: a primary key is
// primary-key, every other index, a unique constraint included, is index.
func indexDependent(table migrate.Name, idx migrate.Index, c engine.ColumnRef) engine.Dependent {
	kind := engine.DependentIndex
	if idx.Primary {
		kind = engine.DependentPrimaryKey
	}
	return engine.Dependent{Kind: kind, Table: table, Name: idx.Name, On: []engine.ColumnRef{c}, Index: new(cloneIndex(idx))}
}

func cloneIndex(idx migrate.Index) migrate.Index {
	idx.Columns = slices.Clone(idx.Columns)
	idx.Included = slices.Clone(idx.Included)
	return idx
}

func cloneForeignKey(fk migrate.ForeignKey) migrate.ForeignKey {
	fk.Columns = slices.Clone(fk.Columns)
	fk.ReferencedColumns = slices.Clone(fk.ReferencedColumns)
	return fk
}

// mergeDependents folds duplicates found through different columns into one
// dependent holding the union of the columns it depends on.
func mergeDependents(deps []engine.Dependent) []engine.Dependent {
	var out []engine.Dependent
	for _, d := range deps {
		i := slices.IndexFunc(out, func(o engine.Dependent) bool {
			return o.Kind == d.Kind && o.Table == d.Table && o.Name == d.Name
		})
		if i < 0 {
			out = append(out, d)
			continue
		}
		out[i].On = append(out[i].On, d.On...)
	}
	for i := range out {
		slices.SortFunc(out[i].On, compareColumnRef)
		out[i].On = slices.Compact(out[i].On)
	}
	slices.SortFunc(out, func(a, b engine.Dependent) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), compareNames(a.Table, b.Table), cmp.Compare(a.Name, b.Name))
	})
	return out
}

func compareColumnRef(a, b engine.ColumnRef) int {
	return cmp.Or(compareNames(a.Table, b.Table), cmp.Compare(a.Column, b.Column))
}

// mentions reports whether a T-SQL expression names the column as a token. It
// does not resolve scope, so it may report a column of another table.
func mentions(expr, column string) (bool, error) {
	tokens, err := sqlglot.Tokenize(expr, atlas.Dialect)
	if err != nil {
		return false, fmt.Errorf("expression %q: %w", expr, err)
	}
	return slices.ContainsFunc(tokens, func(t sqlglot.Token) bool { return strings.EqualFold(t.Text, column) }), nil
}
