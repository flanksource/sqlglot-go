package engine

import (
	"fmt"
	"slices"

	"github.com/flanksource/sqlglot-go/migrate"
)

// preservedBecause says why a drop the diff found is left unplanned, or "" when
// it is planned. A member of a declared table is the declaration's to settle: a
// partial table keeps every member it does not name, whatever the scope, and a
// complete one drops them. Any other drop is the scope's: a declared scope never
// drops an object it did not declare, a complete scope does.
func preservedBecause(c migrate.Change, req PlanRequest) string {
	if c.Action != actionDrop {
		return ""
	}
	if table, ok := declaredTable(req.Desired, c); ok {
		if table.Partial {
			return "a drop of a member of table " + nameLabel(table.Name) + ", which is declared partial"
		}
		return ""
	}
	if req.Scope.Mode == ScopeDeclared {
		return "a drop that the declared scope preserves"
	}
	return ""
}

// declaredTable finds the desired table a member change belongs to.
func declaredTable(desired migrate.Schema, c migrate.Change) (migrate.Table, bool) {
	if c.Member == "" {
		return migrate.Table{}, false
	}
	i := slices.IndexFunc(desired.Tables, func(t migrate.Table) bool { return t.Name == c.Object })
	if i < 0 {
		return migrate.Table{}, false
	}
	return desired.Tables[i], true
}

// checkPartialTables refuses a partial declaration of a table the target does
// not have: it names only some columns, so creating it would create half a
// table. A table inspection refused is left to the refusal check.
func checkPartialTables(req PlanRequest) error {
	for _, t := range req.Desired.Tables {
		if !t.Partial || slices.ContainsFunc(req.Current.Tables, func(c migrate.Table) bool { return c.Name == t.Name }) {
			continue
		}
		if slices.ContainsFunc(req.Refusals, func(r Refusal) bool { return r.Object == t.Name }) {
			continue
		}
		return fmt.Errorf("engine: table %s is declared partial and does not exist: a partial declaration cannot create a table", nameLabel(t.Name))
	}
	return nil
}
