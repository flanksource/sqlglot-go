package atlasdriver

import (
	"fmt"
	"slices"
	"strings"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// Preconditions guards the changes that fail on existing data: a column made
// NOT NULL (any NULL) and a unique index or primary key added to an existing
// table (any duplicate). Each is a query that must return no rows.
func (d *Dialect) Preconditions(change migrate.Change, current, desired migrate.Schema, _ engine.Capabilities) ([]engine.Precondition, error) {
	if !hasTable(current, change.Object) {
		return nil, nil
	}
	switch {
	case change.Kind == "column" && change.Action == "modify":
		return nullToNotNull(change, current, desired), nil
	case (change.Kind == "index" || change.Kind == "primary-key") && (change.Action == "add" || change.Action == "modify"):
		return d.uniqueness(change, desired)
	}
	return nil, nil
}

func hasTable(s migrate.Schema, n migrate.Name) bool {
	return slices.ContainsFunc(s.Tables, func(t migrate.Table) bool { return t.Name == n })
}

func findColumn(s migrate.Schema, table migrate.Name, column string) *migrate.Column {
	for _, t := range s.Tables {
		if t.Name != table {
			continue
		}
		for i := range t.Columns {
			if t.Columns[i].Name == column {
				return &t.Columns[i]
			}
		}
	}
	return nil
}

func nullToNotNull(change migrate.Change, current, desired migrate.Schema) []engine.Precondition {
	before, after := findColumn(current, change.Object, change.Member), findColumn(desired, change.Object, change.Member)
	if before == nil || after == nil || !before.Nullable || after.Nullable {
		return nil
	}
	return []engine.Precondition{{
		ID: change.ID + "#null", Change: change.ID,
		SQL:     fmt.Sprintf("SELECT 1 FROM %s WHERE %s IS NULL LIMIT 1", nameString(change.Object), quoteIdent(change.Member)),
		Message: fmt.Sprintf("column %s.%s has NULL values and cannot become NOT NULL", nameString(change.Object), change.Member),
	}}
}

func (d *Dialect) uniqueness(change migrate.Change, desired migrate.Schema) ([]engine.Precondition, error) {
	s, err := d.decodeCached(desired.Native)
	if err != nil {
		return nil, err
	}
	t, ok := s.Table(change.Object.Name)
	if !ok {
		return nil, fmt.Errorf("%s: change %s names table %s, which the desired schema lacks", d.Name(), change.ID, change.Object.Name)
	}
	primary := change.Kind == "primary-key"
	idx := t.PrimaryKey
	if !primary {
		i := slices.IndexFunc(t.Indexes, func(i *schema.Index) bool { return i.Name == change.Member })
		if i < 0 {
			return nil, fmt.Errorf("%s: change %s names index %s, which the desired table lacks", d.Name(), change.ID, change.Member)
		}
		idx = t.Indexes[i]
	}
	if idx == nil || (!primary && !idx.Unique) {
		return nil, nil
	}
	return d.uniquePreconditions(change, idx, primary)
}

func (d *Dialect) uniquePreconditions(change migrate.Change, idx *schema.Index, primary bool) ([]engine.Precondition, error) {
	var exprs, notNull []string
	for _, p := range idx.Parts {
		switch {
		case p.C != nil:
			exprs = append(exprs, quoteIdent(p.C.Name))
		case p.X != nil:
			exprs = append(exprs, "("+exprText(p.X)+")")
		default:
			return nil, fmt.Errorf("%s: index %s has a part that is neither a column nor an expression", d.Name(), idx.Name)
		}
		notNull = append(notNull, exprs[len(exprs)-1]+" IS NOT NULL")
	}
	table, label := nameString(change.Object), fmt.Sprintf("%s.%s", change.Object.Name, change.Member)
	where := strings.Join(notNull, " AND ")
	if filter := d.atlas.IndexFilter(idx.Attrs); filter != "" {
		where = "(" + filter + ") AND " + where
	}
	out := []engine.Precondition{{
		ID: change.ID + "#duplicate", Change: change.ID,
		SQL:     fmt.Sprintf("SELECT 1 FROM %s WHERE %s GROUP BY %s HAVING count(*) > 1 LIMIT 1", table, where, strings.Join(exprs, ", ")),
		Message: fmt.Sprintf("table %s has duplicate rows for %s (%s)", table, label, strings.Join(exprs, ", ")),
	}}
	if primary {
		nulls := strings.Join(exprs, " IS NULL OR ") + " IS NULL"
		out = append(out, engine.Precondition{
			ID: change.ID + "#null", Change: change.ID,
			SQL:     fmt.Sprintf("SELECT 1 FROM %s WHERE %s LIMIT 1", table, nulls),
			Message: fmt.Sprintf("table %s has NULL values in its primary key columns (%s)", table, strings.Join(exprs, ", ")),
		})
	}
	return out, nil
}

// decodeCached decodes a snapshot for read-only use.
func (d *Dialect) decodeCached(native string) (*schema.Schema, error) {
	if s, ok := d.cache.get(native); ok {
		return s, nil
	}
	s, err := d.decode(native)
	if err != nil {
		return nil, err
	}
	d.cache.put(native, s)
	return s, nil
}
