package sqlserver

import (
	"fmt"
	"regexp"
	"slices"

	"github.com/flanksource/sqlglot-go/migrate"
)

// snapshots are the two schemas an artifact was planned between: the baseline
// its units start from and the state they reach.
type snapshots struct {
	current, desired migrate.Schema
}

// scriptGuard decides whether a change still has to run. An empty apply marks
// an idempotent change that always runs; a non-empty applied names its desired
// state, and a database in neither state is drift.
type scriptGuard struct {
	apply   string
	applied string
}

var (
	moduleKinds     = []string{"view", "procedure", "function", "trigger"}
	moduleTypes     = map[string]string{"table": "U", "view": "V", "procedure": "P", "trigger": "TR", "function": "", "sequence": "SO"}
	constraintKinds = []string{"index", "primary-key", "foreign-key", "check"}
	// systemName matches the names SQL Server generates for unnamed constraints,
	// such as PK__orders__3213E83F5B8D1F2A or FK__items__id__5EBF139D.
	systemName = regexp.MustCompile(`^(PK|UQ|FK|CK|DF)__.+__[0-9A-F]{8}([0-9A-F]{8})?$`)
)

func changeGuard(snaps snapshots, change migrate.Change) (scriptGuard, error) {
	if slices.Contains(constraintKinds, change.Kind) && systemName.MatchString(change.Member) {
		return scriptGuard{}, systemNamed(change, change.Member)
	}
	switch change.Action {
	case "modify":
		return modifyGuard(snaps, change)
	case "add":
		if slices.Contains(moduleKinds, change.Kind) {
			return scriptGuard{}, nil
		}
		if change.Member != "" {
			return addMemberGuard(snaps, change)
		}
		return addObjectGuard(snaps, change)
	case "drop":
		return dropGuard(change)
	}
	return scriptGuard{}, unguardable(change)
}

func systemNamed(change migrate.Change, name string) error {
	return fmt.Errorf("sqlserver: change %s: %s %q is system-named, so its name differs in every database and no script can find it by name; give it an explicit name (sp_rename) on both sides and compare again", change.ID, change.Kind, name)
}

func modifyGuard(snaps snapshots, change migrate.Change) (scriptGuard, error) {
	switch change.Kind {
	case "column":
		return columnGuard(snaps, change)
	case "index", "primary-key":
		return indexGuard(snaps, change)
	case "foreign-key":
		return foreignKeyGuard(snaps, change)
	case "check":
		return checkGuard(snaps, change)
	case "view", "procedure", "function", "trigger", "sequence":
		return scriptGuard{}, nil
	}
	return scriptGuard{}, unguardable(change)
}

// addObjectGuard runs an add while the object is absent and skips it only when
// the object already has exactly its desired shape.
func addObjectGuard(snaps snapshots, change migrate.Change) (scriptGuard, error) {
	switch change.Kind {
	case "table":
		table, ok := findTable(snaps.desired, change.Object)
		if !ok {
			return scriptGuard{}, missingMember(change)
		}
		desired, err := tableState(table)
		return scriptGuard{apply: objectExistence(change.Object, "U").absent, applied: desired}, err
	case "table-type":
		types, err := member(snaps.desired.TableTypes, change, func(t migrate.TableType) bool { return t.Name == change.Object })
		if err != nil {
			return scriptGuard{}, err
		}
		desired, err := tableTypeState(types)
		return scriptGuard{apply: "NOT EXISTS (SELECT 1 FROM sys.table_types WHERE schema_id = SCHEMA_ID(" + literal(change.Object.Schema) + ") AND name = " + literal(change.Object.Name) + ")", applied: desired}, err
	case "sequence":
		sequence, err := member(snaps.desired.Sequences, change, func(q migrate.Sequence) bool { return q.Name == change.Object })
		return scriptGuard{apply: objectExistence(change.Object, "SO").absent, applied: sequenceState(sequence)}, err
	}
	return scriptGuard{}, unguardable(change)
}

// addMemberGuard runs an add while the member is absent and skips it only when
// the member already has exactly its desired shape.
func addMemberGuard(snaps snapshots, change migrate.Change) (scriptGuard, error) {
	desired, ok := findTable(snaps.desired, change.Object)
	if !ok {
		return scriptGuard{}, missingMember(change)
	}
	table := tableObjectID(change.Object)
	switch change.Kind {
	case "column":
		column, err := member(desired.Columns, change, func(c migrate.Column) bool { return c.Name == change.Member })
		if err != nil {
			return scriptGuard{}, err
		}
		state, err := columnState(table, column)
		return scriptGuard{apply: memberExistence(change).absent, applied: state}, err
	case "index", "primary-key":
		index, err := member(desired.Indexes, change, indexMatch(change))
		return scriptGuard{apply: memberExistence(change).absent, applied: indexState(table, index)}, err
	case "foreign-key":
		key, err := member(desired.ForeignKeys, change, func(f migrate.ForeignKey) bool { return f.Name == change.Member })
		return scriptGuard{apply: memberExistence(change).absent, applied: foreignKeyState(change.Object, key)}, err
	case "check":
		check, err := member(desired.Checks, change, func(c migrate.Check) bool { return c.Name == change.Member })
		return scriptGuard{apply: memberExistence(change).absent, applied: checkState(table, check)}, err
	}
	return scriptGuard{}, unguardable(change)
}

// dropGuard runs a drop while the object or member exists. A primary key is
// found by name, so a table whose key has another name is drift.
func dropGuard(change migrate.Change) (scriptGuard, error) {
	switch {
	case change.Kind == "primary-key":
		return scriptGuard{apply: memberExistence(change).present, applied: "NOT EXISTS (" + primaryKeyQuery(change.Object) + ")"}, nil
	case change.Kind == "table-type":
		return scriptGuard{apply: "EXISTS (SELECT 1 FROM sys.table_types WHERE schema_id = SCHEMA_ID(" + literal(change.Object.Schema) + ") AND name = " + literal(change.Object.Name) + ")"}, nil
	case change.Member != "":
		return scriptGuard{apply: memberExistence(change).present}, nil
	}
	if objectType, ok := moduleTypes[change.Kind]; ok {
		return scriptGuard{apply: objectExistence(change.Object, objectType).present}, nil
	}
	return scriptGuard{}, unguardable(change)
}

type existence struct{ absent, present string }

func subquery(query string) existence {
	return existence{absent: "NOT EXISTS (" + query + ")", present: "EXISTS (" + query + ")"}
}

// memberExistence finds a table member by name, and an added primary key as
// any primary key of the table.
func memberExistence(change migrate.Change) existence {
	table := literal(qualified(change.Object))
	switch change.Kind {
	case "column":
		return existence{absent: "COL_LENGTH(" + table + ", " + literal(change.Member) + ") IS NULL", present: "COL_LENGTH(" + table + ", " + literal(change.Member) + ") IS NOT NULL"}
	case "primary-key":
		if change.Action == "add" {
			return subquery(primaryKeyQuery(change.Object))
		}
		return subquery("SELECT 1 FROM sys.indexes WHERE object_id = OBJECT_ID(" + table + ") AND name = " + literal(change.Member))
	case "index":
		return subquery("SELECT 1 FROM sys.indexes WHERE object_id = OBJECT_ID(" + table + ") AND name = " + literal(change.Member))
	}
	catalog := map[string]string{"foreign-key": "sys.foreign_keys", "check": "sys.check_constraints"}[change.Kind]
	return subquery("SELECT 1 FROM " + catalog + " WHERE parent_object_id = OBJECT_ID(" + table + ") AND name = " + literal(change.Member))
}

func primaryKeyQuery(table migrate.Name) string {
	return "SELECT 1 FROM sys.indexes WHERE object_id = " + tableObjectID(table) + " AND is_primary_key = 1"
}

func objectExistence(name migrate.Name, objectType string) existence {
	id := "OBJECT_ID(" + literal(qualified(name)) + ")"
	if objectType != "" {
		id = "OBJECT_ID(" + literal(qualified(name)) + ", " + literal(objectType) + ")"
	}
	return existence{absent: id + " IS NULL", present: id + " IS NOT NULL"}
}

func unguardable(change migrate.Change) error {
	return fmt.Errorf("sqlserver: change %s: no state guard can be derived for %s %s", change.ID, change.Action, change.Kind)
}

func missingMember(change migrate.Change) error {
	return fmt.Errorf("sqlserver: change %s: %s %q is missing from the baseline or desired snapshot", change.ID, change.Kind, change.Member)
}

func findTable(schema migrate.Schema, name migrate.Name) (migrate.Table, bool) {
	i := slices.IndexFunc(schema.Tables, func(t migrate.Table) bool { return t.Name == name })
	if i < 0 {
		return migrate.Table{}, false
	}
	return schema.Tables[i], true
}

func member[T any](list []T, change migrate.Change, match func(T) bool) (T, error) {
	i := slices.IndexFunc(list, match)
	if i < 0 {
		var zero T
		return zero, missingMember(change)
	}
	return list[i], nil
}

func indexMatch(change migrate.Change) func(migrate.Index) bool {
	if change.Kind == "primary-key" {
		return func(i migrate.Index) bool { return i.Primary }
	}
	return func(i migrate.Index) bool { return i.Name == change.Member }
}

// memberStates finds a modified member in the baseline and desired tables.
func memberStates[T any](snaps snapshots, change migrate.Change, members func(migrate.Table) []T, match func(T) bool) (T, T, error) {
	found := make([]T, 0, 2)
	for _, schema := range []migrate.Schema{snaps.current, snaps.desired} {
		table, ok := findTable(schema, change.Object)
		if !ok {
			var zero T
			return zero, zero, missingMember(change)
		}
		state, err := member(members(table), change, match)
		if err != nil {
			return state, state, err
		}
		found = append(found, state)
	}
	return found[0], found[1], nil
}

func columnGuard(snaps snapshots, change migrate.Change) (scriptGuard, error) {
	from, to, err := memberStates(snaps, change, func(t migrate.Table) []migrate.Column { return t.Columns },
		func(c migrate.Column) bool { return c.Name == change.Member })
	if err != nil {
		return scriptGuard{}, err
	}
	table := tableObjectID(change.Object)
	apply, err := columnState(table, from)
	if err != nil {
		return scriptGuard{}, fmt.Errorf("sqlserver: change %s: %w", change.ID, err)
	}
	applied, err := columnState(table, to)
	if err != nil {
		return scriptGuard{}, fmt.Errorf("sqlserver: change %s: %w", change.ID, err)
	}
	return scriptGuard{apply: apply, applied: applied}, nil
}

func indexGuard(snaps snapshots, change migrate.Change) (scriptGuard, error) {
	from, to, err := memberStates(snaps, change, func(t migrate.Table) []migrate.Index { return t.Indexes }, indexMatch(change))
	if err != nil {
		return scriptGuard{}, err
	}
	if systemName.MatchString(from.Name) {
		return scriptGuard{}, systemNamed(change, from.Name)
	}
	table := tableObjectID(change.Object)
	return scriptGuard{apply: indexState(table, from), applied: indexState(table, to)}, nil
}

func foreignKeyGuard(snaps snapshots, change migrate.Change) (scriptGuard, error) {
	from, to, err := memberStates(snaps, change, func(t migrate.Table) []migrate.ForeignKey { return t.ForeignKeys },
		func(f migrate.ForeignKey) bool { return f.Name == change.Member })
	if err != nil {
		return scriptGuard{}, err
	}
	return scriptGuard{apply: foreignKeyState(change.Object, from), applied: foreignKeyState(change.Object, to)}, nil
}

// checkGuard matches the baseline's and the desired constraint's definitions
// exactly as sys.check_constraints stores them.
func checkGuard(snaps snapshots, change migrate.Change) (scriptGuard, error) {
	from, to, err := memberStates(snaps, change, func(t migrate.Table) []migrate.Check { return t.Checks },
		func(c migrate.Check) bool { return c.Name == change.Member })
	if err != nil {
		return scriptGuard{}, err
	}
	table := tableObjectID(change.Object)
	return scriptGuard{apply: checkState(table, from), applied: checkState(table, to)}, nil
}
