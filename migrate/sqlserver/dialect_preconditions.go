package sqlserver

import (
	"fmt"
	"slices"
	"strings"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
	atlas "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// guidPattern is a canonical GUID: 8-4-4-4-12 hexadecimal digits.
var guidPattern = func() string {
	hex := "[0-9A-Fa-f]"
	groups := make([]string, 0, 5)
	for _, n := range []int{8, 4, 4, 4, 12} {
		groups = append(groups, strings.Repeat(hex, n))
	}
	return strings.Join(groups, "-")
}()

// Preconditions returns the guards a change needs: each is a query that
// returns a row when the data would make the change fail or lose data. They
// are table scans, and stop at the first offending row.
func (Planner) Preconditions(change migrate.Change, current, desired migrate.Schema, caps engine.Capabilities) ([]engine.Precondition, error) {
	switch {
	case change.Kind == "column" && change.Action == "modify":
		return columnGuards(change, current, desired, caps)
	case (change.Kind == "index" || change.Kind == "primary-key") && (change.Action == "add" || change.Action == "modify"):
		return uniquenessGuards(change, current, desired)
	}
	return nil, nil
}

func guard(change migrate.Change, suffix, sql, message string) engine.Precondition {
	return engine.Precondition{ID: change.ID + "#" + suffix, Change: change.ID, SQL: sql, Message: message}
}

func columnLabel(change migrate.Change) string { return qualified(change.Object) + "." + change.Member }

func findColumn(s migrate.Schema, change migrate.Change, side string) (migrate.Column, error) {
	t, ok := findTable(s, change.Object)
	if ok {
		if i := slices.IndexFunc(t.Columns, func(c migrate.Column) bool { return c.Name == change.Member }); i >= 0 {
			return t.Columns[i], nil
		}
	}
	return migrate.Column{}, fmt.Errorf("sqlserver: change %s: column %s is not in the %s schema", change.ID, columnLabel(change), side)
}

func columnGuards(change migrate.Change, current, desired migrate.Schema, caps engine.Capabilities) ([]engine.Precondition, error) {
	from, err := findColumn(current, change, "current")
	if err != nil {
		return nil, err
	}
	to, err := findColumn(desired, change, "desired")
	if err != nil {
		return nil, err
	}
	fromType, err := atlas.ParseType(from.Type)
	if err != nil {
		return nil, fmt.Errorf("sqlserver: change %s: current type: %w", change.ID, err)
	}
	toType, err := atlas.ParseType(to.Type)
	if err != nil {
		return nil, fmt.Errorf("sqlserver: change %s: desired type: %w", change.ID, err)
	}
	table, col := qualified(change.Object), quote(change.Member)
	var out []engine.Precondition
	if isText(fromType) {
		if _, ok := toType.(*schema.UUIDType); ok {
			out = append(out, guidGuard(change, table, col, caps))
		}
	}
	if from.Nullable && !to.Nullable {
		out = append(out, guard(change, "not-null",
			fmt.Sprintf("SELECT TOP (1) 1 FROM %s WHERE %s IS NULL", table, col),
			columnLabel(change)+" holds NULL values, which NOT NULL would reject"))
	}
	if limit, expr, ok := narrowedLength(fromType, toType, col); ok {
		out = append(out, guard(change, "narrow",
			fmt.Sprintf("SELECT TOP (1) 1 FROM %s WHERE %s > %d", table, expr, limit),
			fmt.Sprintf("%s holds values longer than the new length %d", columnLabel(change), limit)))
	}
	return out, nil
}

func isText(t schema.Type) bool {
	s, ok := t.(*schema.StringType)
	return ok && slices.Contains([]string{"char", "varchar", "nchar", "nvarchar"}, s.T)
}

func guidGuard(change migrate.Change, table, col string, caps engine.Capabilities) engine.Precondition {
	invalid := fmt.Sprintf("LEN(%s) <> 36 OR %s NOT LIKE '%s'", col, col, guidPattern)
	if caps.Features[FeatureTryConvert] {
		invalid = fmt.Sprintf("TRY_CONVERT(uniqueidentifier, %s) IS NULL", col)
	}
	return guard(change, "guid",
		fmt.Sprintf("SELECT TOP (1) 1 FROM %s WHERE %s IS NOT NULL AND (%s)", table, col, invalid),
		columnLabel(change)+" holds values that are not canonical GUIDs, which uniqueidentifier would reject")
}

// narrowedLength reports a character or binary column that gets a smaller
// fixed length, with the length and the expression that measures a stored
// value in the new type's units (characters for the n-types, else bytes).
func narrowedLength(from, to schema.Type, col string) (limit int, measure string, ok bool) {
	f, fromOK := lengthOf(from)
	t, toOK := lengthOf(to)
	if !fromOK || !toOK || f.binary != t.binary || t.size < 0 || f.size >= 0 && f.size <= t.size {
		return 0, "", false
	}
	if f.wide {
		return t.size, "DATALENGTH(" + col + ") / 2", true
	}
	return t.size, "DATALENGTH(" + col + ")", true
}

type lengthType struct {
	size         int // -1 is (max)
	wide, binary bool
}

func lengthOf(t schema.Type) (lengthType, bool) {
	switch t := t.(type) {
	case *schema.StringType:
		if isText(t) {
			return lengthType{size: t.Size, wide: t.T == "nchar" || t.T == "nvarchar"}, true
		}
	case *schema.BinaryType:
		if (t.T == "binary" || t.T == "varbinary") && t.Size != nil {
			return lengthType{size: *t.Size, binary: true}, true
		}
	}
	return lengthType{}, false
}

// uniquenessGuards finds a duplicate key where a new unique index, unique
// constraint or primary key would be built over rows that already exist. It
// is skipped when the key reaches a column the current table lacks, which the
// same plan adds.
func uniquenessGuards(change migrate.Change, current, desired migrate.Schema) ([]engine.Precondition, error) {
	before, exists := findTable(current, change.Object)
	if !exists {
		return nil, nil
	}
	after, ok := findTable(desired, change.Object)
	if !ok {
		return nil, fmt.Errorf("sqlserver: change %s: table %s is not in the desired schema", change.ID, qualified(change.Object))
	}
	idx, ok := findIndex(after, change.Member)
	if !ok || !enforcesUniqueness(idx) || !newUniqueness(before, idx) {
		return nil, nil
	}
	keys := make([]string, len(idx.Columns))
	for i, c := range idx.Columns {
		if !slices.ContainsFunc(before.Columns, func(col migrate.Column) bool { return col.Name == c.Name }) {
			return nil, nil
		}
		keys[i] = quote(c.Name)
	}
	sql := "SELECT TOP (1) 1 FROM " + qualified(change.Object)
	if idx.Filter != "" {
		sql += " WHERE " + idx.Filter
	}
	sql += " GROUP BY " + strings.Join(keys, ", ") + " HAVING COUNT(*) > 1"
	return []engine.Precondition{guard(change, "unique", sql, fmt.Sprintf("%s holds duplicate values of (%s), which unique index %s would reject",
		qualified(change.Object), strings.Join(keys, ", "), quote(idx.Name)))}, nil
}

func findIndex(t migrate.Table, name string) (migrate.Index, bool) {
	i := slices.IndexFunc(t.Indexes, func(idx migrate.Index) bool { return idx.Name == name })
	if i < 0 {
		return migrate.Index{}, false
	}
	return t.Indexes[i], true
}

// newUniqueness reports an index that enforces more than the live table's
// index of the same name does.
func newUniqueness(before migrate.Table, idx migrate.Index) bool {
	live, ok := findIndex(before, idx.Name)
	if !ok || !enforcesUniqueness(live) {
		return true
	}
	return live.Filter != idx.Filter || !slices.Equal(live.Columns, idx.Columns)
}

// enforcesUniqueness reports an index that rejects duplicate keys.
func enforcesUniqueness(idx migrate.Index) bool { return idx.Unique || idx.Primary || idx.Constraint }
