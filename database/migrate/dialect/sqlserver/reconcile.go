package sqlserver

import (
	"context"
	"slices"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// Reconcile rewrites desired so that what it leaves unset inherits the live
// value and what the server compares as equal is spelled like the live value.
// It touches nothing else: a real difference stays one. The rules:
//   - a column of a character type with no collation takes the live column's
//     collation, and a collation that differs only in case takes the live spelling.
func (Dialect) Reconcile(_ context.Context, _ engine.Querier, current, desired migrate.Schema) (migrate.Schema, error) {
	out := desired
	out.Tables = slices.Clone(desired.Tables)
	for i := range out.Tables {
		live, ok := findTable(current, out.Tables[i].Name)
		if !ok {
			continue
		}
		out.Tables[i].Columns = reconcileColumns(live, out.Tables[i].Columns)
	}
	return out, nil
}

func findTable(s migrate.Schema, name migrate.Name) (migrate.Table, bool) {
	i := slices.IndexFunc(s.Tables, func(t migrate.Table) bool { return t.Name == name })
	if i < 0 {
		return migrate.Table{}, false
	}
	return s.Tables[i], true
}

func reconcileColumns(live migrate.Table, desired []migrate.Column) []migrate.Column {
	out := slices.Clone(desired)
	for i, want := range out {
		j := slices.IndexFunc(live.Columns, func(c migrate.Column) bool { return c.Name == want.Name })
		if j >= 0 {
			out[i] = reconcileColumn(live.Columns[j], want)
		}
	}
	return out
}

func reconcileColumn(live, want migrate.Column) migrate.Column {
	switch {
	case !isCharacterType(want.Type):
	case want.Collation == "":
		want.Collation = live.Collation
	case strings.EqualFold(want.Collation, live.Collation):
		want.Collation = live.Collation
	}
	return want
}

// isCharacterType reports a type that carries a collation.
func isCharacterType(typ string) bool {
	name := strings.ToLower(strings.TrimSpace(typ))
	for _, prefix := range []string{"varchar", "nvarchar", "char", "nchar", "text", "ntext"} {
		if name == prefix || strings.HasPrefix(name, prefix+"(") {
			return true
		}
	}
	return false
}
