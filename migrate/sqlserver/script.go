package sqlserver

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
	atlas "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
)

// SessionOptions are the SET options every migration statement runs under,
// whether the executor applies an artifact or a rendered script does.
const SessionOptions = "SET XACT_ABORT ON; SET ANSI_NULLS ON; SET ANSI_PADDING ON; SET ANSI_WARNINGS ON; SET ARITHABORT ON; SET CONCAT_NULL_YIELDS_NULL ON; SET QUOTED_IDENTIFIER ON; SET NUMERIC_ROUNDABORT OFF;"

// restoreSessionOptions puts back, after the commit, each runtime option that
// SessionOptions changed, from the @@OPTIONS bits saved before it. SQL Server
// applies SET QUOTED_IDENTIFIER while it parses the batch, so a conditional SET
// cannot restore it; it stays ON. An error aborts the batch before this runs.
const restoreSessionOptions = "IF @options & 16384 = 0 SET XACT_ABORT OFF;\n" +
	"IF @options & 32 = 0 SET ANSI_NULLS OFF;\n" +
	"IF @options & 16 = 0 SET ANSI_PADDING OFF;\n" +
	"IF @options & 8 = 0 SET ANSI_WARNINGS OFF;\n" +
	"IF @options & 64 = 0 SET ARITHABORT OFF;\n" +
	"IF @options & 4096 = 0 SET CONCAT_NULL_YIELDS_NULL OFF;\n" +
	"IF @options & 8192 <> 0 SET NUMERIC_ROUNDABORT ON;\n"

// LockResource is the transaction-owned applock that serialises every
// migration of a database, applied directly or through a rendered script.
const LockResource = "commons-db/migrate/schema"

const scriptLockTimeoutMillis = 30000

type ScriptOptions struct {
	// Acknowledgements lists every destructive change ID exactly once.
	Acknowledgements []string
}

// ValidateAcknowledgements requires each destructive change of an artifact to
// be acknowledged exactly once and refuses unknown or repeated IDs.
func ValidateAcknowledgements(artifact migrate.Artifact, acknowledgements []string) error {
	required := map[string]bool{}
	for _, id := range Acknowledgements(artifact) {
		required[id] = true
	}
	for _, id := range acknowledgements {
		if !required[id] {
			return fmt.Errorf("unknown or duplicate destructive acknowledgement %q", id)
		}
		delete(required, id)
	}
	if len(required) > 0 {
		return fmt.Errorf("migration requires acknowledgements for %d destructive changes", len(required))
	}
	return nil
}

var batchSeparator = regexp.MustCompile(`(?mi)^\s*GO\s*$`)

// RenderScript renders a reviewed artifact as one re-runnable T-SQL batch: a
// single transaction under LockResource that evaluates every change's state
// guard against the database first, then runs each step whose change is still
// at its baseline. It has no GO separators, so a transaction never spans batches.
func RenderScript(artifact migrate.Artifact, options ScriptOptions) (string, error) {
	if err := ValidateArtifact(artifact); err != nil {
		return "", err
	}
	if err := ValidateAcknowledgements(artifact, options.Acknowledgements); err != nil {
		return "", err
	}
	changes := stepChanges(artifact.Plan.Steps)
	var b strings.Builder
	if err := writeScriptHeader(&b, artifact, changes, options.Acknowledgements); err != nil {
		return "", err
	}
	b.WriteString("DECLARE @options int = @@OPTIONS;\n" + SessionOptions + "\nBEGIN TRANSACTION;\nDECLARE @lock int, @df sysname, @sql nvarchar(max);\n")
	fmt.Fprintf(&b, "EXEC @lock = sys.sp_getapplock @Resource = %s, @LockMode = 'Exclusive', @LockOwner = 'Transaction', @LockTimeout = %d;\n", literal(LockResource), scriptLockTimeoutMillis)
	fmt.Fprintf(&b, "IF @lock < 0 THROW 50000, %s, 1;\n", literal(fmt.Sprintf("migration %s: sp_getapplock on %s failed", artifact.Hash, LockResource)))
	flags := map[string]string{}
	for i, change := range changes {
		flags[change.ID] = "@c" + strconv.Itoa(i+1)
		block, err := guardBlock(artifact, change, flags[change.ID])
		if err != nil {
			return "", err
		}
		b.WriteString(block)
	}
	for _, group := range interdependentChanges(artifact.Request, artifact.Plan.Steps) {
		b.WriteString(groupCheck(artifact.Hash, group, flags))
	}
	for _, step := range artifact.Plan.Steps {
		statement, err := stepStatement(artifact.Request, step, flags[step.Change.ID])
		if err != nil {
			return "", err
		}
		b.WriteString(statement)
	}
	b.WriteString("COMMIT TRANSACTION;\n" + restoreSessionOptions)
	if batchSeparator.MatchString(b.String()) {
		return "", fmt.Errorf("sqlserver: migration %s: a step contains a GO line, which would split the script into batches", artifact.Hash)
	}
	return b.String(), nil
}

// stepChanges lists the changes the steps implement, in first-step order.
func stepChanges(steps []migrate.Step) []migrate.Change {
	var changes []migrate.Change
	for _, step := range steps {
		if !slices.ContainsFunc(changes, func(c migrate.Change) bool { return c.ID == step.Change.ID }) {
			changes = append(changes, step.Change)
		}
	}
	return changes
}

func writeScriptHeader(b *strings.Builder, artifact migrate.Artifact, changes []migrate.Change, acknowledgements []string) error {
	schemas, err := json.Marshal(artifact.Schemas)
	if err != nil {
		return err
	}
	fmt.Fprintf(b, "-- sqlglot SQL Server migration artifact %s\n", artifact.Hash)
	b.WriteString("-- Generated by sqlglot-go RenderScript. Nothing verifies the hash when the script runs: a hand-edited script is no longer the reviewed artifact, so re-render it instead of editing it.\n")
	b.WriteString("-- Session options are restored after COMMIT, except QUOTED_IDENTIFIER, which stays ON; a failed run leaves them as this script sets them.\n")
	fmt.Fprintf(b, "-- Schemas: %s\n", schemas)
	writeHeaderList(b, "Changes", changeIDs(changes))
	acknowledged := slices.Clone(acknowledgements)
	slices.Sort(acknowledged)
	writeHeaderList(b, "Acknowledged destructive changes", acknowledged)
	return nil
}

func writeHeaderList(b *strings.Builder, title string, ids []string) {
	if len(ids) == 0 {
		fmt.Fprintf(b, "-- %s: none\n", title)
		return
	}
	fmt.Fprintf(b, "-- %s:\n", title)
	for _, id := range ids {
		fmt.Fprintf(b, "--   %s\n", id)
	}
}

// scriptGuard decides whether a change still has to run. An empty apply marks
// an idempotent change that always runs; a non-empty applied names its desired
// state, and a database in neither state is drift.
type scriptGuard struct {
	apply   string
	applied string
}

func guardBlock(artifact migrate.Artifact, change migrate.Change, variable string) (string, error) {
	guard, err := changeGuard(artifact.Request, change)
	if err != nil {
		return "", err
	}
	header := "-- " + change.ID + "\nDECLARE " + variable + " bit = 0;\n"
	if guard.apply == "" {
		if err := requireIdempotent(artifact.Plan.Steps, change); err != nil {
			return "", err
		}
		return header + "SET " + variable + " = 1;\n", nil
	}
	block := header + "IF " + guard.apply + " SET " + variable + " = 1;\n"
	if guard.applied != "" {
		message := fmt.Sprintf("migration %s: change %s matches neither its baseline nor its desired state", artifact.Hash, change.ID)
		block += "ELSE IF NOT " + guard.applied + " THROW 50000, " + literal(message) + ", 1;\n"
	}
	return block, nil
}

// requireIdempotent checks that every step of an unguarded change is a
// statement that converges when it runs again.
func requireIdempotent(steps []migrate.Step, change migrate.Change) error {
	for _, step := range steps {
		if step.Change.ID != change.ID {
			continue
		}
		upper := strings.ToUpper(step.SQL)
		if !strings.Contains(upper, "CREATE OR ALTER ") && !strings.HasPrefix(upper, "ENABLE TRIGGER ") &&
			!strings.HasPrefix(upper, "DISABLE TRIGGER ") && !strings.HasPrefix(upper, "ALTER SEQUENCE ") {
			return fmt.Errorf("sqlserver: change %s: step %q is not re-runnable and has no state guard", change.ID, step.SQL)
		}
	}
	return nil
}

// interdependentChanges groups changes whose flags must agree: a change whose
// steps are split around the ALTER COLUMN of a column it references, such as an
// index dropped before and re-created after it, cannot run when only one of
// them is already in its desired state.
func interdependentChanges(request migrate.Request, steps []migrate.Step) [][]string {
	first, last := map[string]int{}, map[string]int{}
	for i := len(steps) - 1; i >= 0; i-- {
		first[steps[i].Change.ID] = i
	}
	for i, step := range steps {
		last[step.Change.ID] = i
	}
	var groups [][]string
	for i, step := range steps {
		if first[step.Change.ID] != i || last[step.Change.ID] <= i+1 {
			continue
		}
		for _, between := range steps[i+1 : last[step.Change.ID]] {
			column := between.Change
			if column.Kind == "column" && column.Action == "modify" && column.Object == step.Change.Object && references(request, step.Change, column.Member) {
				groups = joinGroups(groups, step.Change.ID, column.ID)
			}
		}
	}
	for _, group := range groups {
		slices.SortFunc(group, func(a, b string) int { return first[a] - first[b] })
	}
	return groups
}

func joinGroups(groups [][]string, a, b string) [][]string {
	i := slices.IndexFunc(groups, func(g []string) bool { return slices.Contains(g, a) })
	j := slices.IndexFunc(groups, func(g []string) bool { return slices.Contains(g, b) })
	switch {
	case i < 0 && j < 0:
		return append(groups, []string{a, b})
	case i == j:
		return groups
	case i < 0:
		groups[j] = append(groups[j], a)
	case j < 0:
		groups[i] = append(groups[i], b)
	default:
		groups[i] = append(groups[i], groups[j]...)
		return slices.Delete(groups, j, j+1)
	}
	return groups
}

// references reports whether a member change mentions a column in either
// snapshot; a kind it cannot inspect counts as a reference.
func references(request migrate.Request, change migrate.Change, column string) bool {
	named := func(name string) bool { return strings.EqualFold(name, column) }
	mentions := func(text string) bool { return strings.Contains(strings.ToLower(text), strings.ToLower(column)) }
	for _, snapshot := range []migrate.Schema{request.Current, request.Desired} {
		table, _ := findTable(snapshot, change.Object)
		switch change.Kind {
		case "index", "primary-key":
			for _, index := range table.Indexes {
				if (index.Name == change.Member || change.Kind == "primary-key" && index.Primary) &&
					(slices.ContainsFunc(index.Columns, func(c migrate.IndexColumn) bool { return named(c.Name) }) || slices.ContainsFunc(index.Included, named) || mentions(index.Filter)) {
					return true
				}
			}
		case "foreign-key":
			if slices.ContainsFunc(table.ForeignKeys, func(f migrate.ForeignKey) bool {
				return f.Name == change.Member && slices.ContainsFunc(f.Columns, named)
			}) {
				return true
			}
		case "check":
			if slices.ContainsFunc(table.Checks, func(c migrate.Check) bool { return c.Name == change.Member && mentions(c.Expression) }) {
				return true
			}
		default:
			return true
		}
	}
	return false
}

func groupCheck(hash string, ids []string, flags map[string]string) string {
	var equal []string
	for _, id := range ids[1:] {
		equal = append(equal, flags[ids[0]]+" = "+flags[id])
	}
	message := fmt.Sprintf("migration %s: changes %s depend on each other and only some of them are in their desired state", hash, strings.Join(ids, ", "))
	return "IF NOT (" + strings.Join(equal, " AND ") + ") THROW 50000, " + literal(message) + ", 1;\n"
}

// stepStatement runs one step under its change's flag. A step that drops a
// column's baseline default resolves the constraint's name when the script
// runs, because a default's name differs between databases.
func stepStatement(request migrate.Request, step migrate.Step, flag string) (string, error) {
	column, err := droppedDefault(request, step)
	if err != nil || column == "" {
		return "IF " + flag + " = 1 EXEC sys.sp_executesql " + literal(step.SQL) + ";\n", err
	}
	table := qualified(step.Change.Object)
	return "IF " + flag + " = 1\nBEGIN\nSET @df = NULL;\n" +
		"SELECT @df = dc.name FROM sys.default_constraints AS dc JOIN sys.columns AS c ON c.object_id = dc.parent_object_id AND c.column_id = dc.parent_column_id" +
		" WHERE c.object_id = OBJECT_ID(" + literal(table) + ") AND c.name = " + literal(column) + ";\n" +
		"IF @df IS NOT NULL\nBEGIN\nSET @sql = " + literal("ALTER TABLE "+table+" DROP CONSTRAINT ") + " + QUOTENAME(@df);\nEXEC sys.sp_executesql @sql;\nEND\nEND\n", nil
}

// droppedDefault names the column whose baseline default constraint a column
// change's step drops, and refuses any other constraint drop in such a change.
func droppedDefault(request migrate.Request, step migrate.Step) (string, error) {
	change := step.Change
	if change.Kind != "column" || !strings.Contains(strings.ToUpper(step.SQL), " DROP CONSTRAINT ") {
		return "", nil
	}
	table, _ := findTable(request.Current, change.Object)
	column, err := member(table.Columns, change, func(c migrate.Column) bool { return c.Name == change.Member })
	if err != nil {
		return "", err
	}
	if column.Default == nil || step.SQL != "ALTER TABLE "+qualified(change.Object)+" DROP CONSTRAINT "+quote(column.Default.Name) {
		return "", fmt.Errorf("sqlserver: change %s: step %q drops a constraint that is not the baseline default of column %q", change.ID, step.SQL, change.Member)
	}
	return column.Name, nil
}

var (
	moduleKinds     = []string{"view", "procedure", "function", "trigger"}
	moduleTypes     = map[string]string{"table": "U", "view": "V", "procedure": "P", "trigger": "TR", "function": "", "sequence": "SO"}
	constraintKinds = []string{"index", "primary-key", "foreign-key", "check"}
	// systemName matches the names SQL Server generates for unnamed constraints,
	// such as PK__orders__3213E83F5B8D1F2A or FK__items__id__5EBF139D.
	systemName = regexp.MustCompile(`^(PK|UQ|FK|CK|DF)__.+__[0-9A-F]{8}([0-9A-F]{8})?$`)
)

func changeGuard(request migrate.Request, change migrate.Change) (scriptGuard, error) {
	if slices.Contains(constraintKinds, change.Kind) && systemName.MatchString(change.Member) {
		return scriptGuard{}, systemNamed(change, change.Member)
	}
	switch change.Action {
	case "modify":
		return modifyGuard(request, change)
	case "add":
		if slices.Contains(moduleKinds, change.Kind) {
			return scriptGuard{}, nil
		}
		if change.Member != "" {
			return addMemberGuard(request, change)
		}
		return addObjectGuard(request, change)
	case "drop":
		return dropGuard(change)
	}
	return scriptGuard{}, unguardable(change)
}

func systemNamed(change migrate.Change, name string) error {
	return fmt.Errorf("sqlserver: change %s: %s %q is system-named, so its name differs in every database and no script can find it by name; give it an explicit name (sp_rename) on both sides and compare again", change.ID, change.Kind, name)
}

func modifyGuard(request migrate.Request, change migrate.Change) (scriptGuard, error) {
	switch change.Kind {
	case "column":
		return columnGuard(request, change)
	case "index", "primary-key":
		return indexGuard(request, change)
	case "foreign-key":
		return foreignKeyGuard(request, change)
	case "check":
		return checkGuard(request, change)
	case "view", "procedure", "function", "trigger", "sequence":
		return scriptGuard{}, nil
	}
	return scriptGuard{}, unguardable(change)
}

// addObjectGuard runs an add while the object is absent and skips it only when
// the object already has exactly its desired shape.
func addObjectGuard(request migrate.Request, change migrate.Change) (scriptGuard, error) {
	switch change.Kind {
	case "table":
		table, ok := findTable(request.Desired, change.Object)
		if !ok {
			return scriptGuard{}, missingMember(change)
		}
		desired, err := tableState(table)
		return scriptGuard{apply: objectExistence(change.Object, "U").absent, applied: desired}, err
	case "table-type":
		types, err := member(request.Desired.TableTypes, change, func(t migrate.TableType) bool { return t.Name == change.Object })
		if err != nil {
			return scriptGuard{}, err
		}
		desired, err := tableTypeState(types)
		return scriptGuard{apply: "NOT EXISTS (SELECT 1 FROM sys.table_types WHERE schema_id = SCHEMA_ID(" + literal(change.Object.Schema) + ") AND name = " + literal(change.Object.Name) + ")", applied: desired}, err
	case "sequence":
		sequence, err := member(request.Desired.Sequences, change, func(q migrate.Sequence) bool { return q.Name == change.Object })
		return scriptGuard{apply: objectExistence(change.Object, "SO").absent, applied: sequenceState(sequence)}, err
	}
	return scriptGuard{}, unguardable(change)
}

// addMemberGuard runs an add while the member is absent and skips it only when
// the member already has exactly its desired shape.
func addMemberGuard(request migrate.Request, change migrate.Change) (scriptGuard, error) {
	desired, ok := findTable(request.Desired, change.Object)
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

func findTable(snapshot migrate.Schema, name migrate.Name) (migrate.Table, bool) {
	i := slices.IndexFunc(snapshot.Tables, func(t migrate.Table) bool { return t.Name == name })
	if i < 0 {
		return migrate.Table{}, false
	}
	return snapshot.Tables[i], true
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
func memberStates[T any](request migrate.Request, change migrate.Change, members func(migrate.Table) []T, match func(T) bool) (T, T, error) {
	found := make([]T, 0, 2)
	for _, snapshot := range []migrate.Schema{request.Current, request.Desired} {
		table, ok := findTable(snapshot, change.Object)
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

func tableObjectID(table migrate.Name) string { return "OBJECT_ID(" + literal(qualified(table)) + ")" }

func countIs(from string, n int) string {
	return "(SELECT COUNT(*) FROM " + from + ") = " + strconv.Itoa(n)
}

// tableState matches a whole table: its column, index, foreign key and check
// counts and each member's exact desired state. System-named members match by
// shape, never by name.
func tableState(table migrate.Table) (string, error) {
	id := tableObjectID(table.Name)
	var conditions []string
	for _, column := range table.Columns {
		state, err := columnState(id, column)
		if err != nil {
			return "", fmt.Errorf("table %s: %w", qualified(table.Name), err)
		}
		conditions = append(conditions, state)
	}
	for _, index := range table.Indexes {
		conditions = append(conditions, indexState(id, index))
	}
	for _, key := range table.ForeignKeys {
		conditions = append(conditions, foreignKeyState(table.Name, key))
	}
	for _, check := range table.Checks {
		conditions = append(conditions, checkState(id, check))
	}
	conditions = append(conditions,
		countIs("sys.columns WHERE object_id = "+id, len(table.Columns)),
		countIs("sys.indexes WHERE object_id = "+id+" AND type > 0 AND is_hypothetical = 0", len(table.Indexes)),
		countIs("sys.foreign_keys WHERE parent_object_id = "+id, len(table.ForeignKeys)),
		countIs("sys.check_constraints WHERE parent_object_id = "+id, len(table.Checks)),
	)
	return "(" + strings.Join(conditions, " AND ") + ")", nil
}

// tableTypeState matches a table type's columns, keys and checks. SQL Server
// names a table type's constraints itself, so keys and checks match by shape.
func tableTypeState(t migrate.TableType) (string, error) {
	id := "(SELECT tt.type_table_object_id FROM sys.table_types AS tt WHERE tt.schema_id = SCHEMA_ID(" + literal(t.Name.Schema) + ") AND tt.name = " + literal(t.Name.Name) + ")"
	conditions := []string{
		countIs("sys.columns WHERE object_id = "+id, len(t.Columns)),
		countIs("sys.indexes WHERE object_id = "+id+" AND index_id > 0", len(t.Keys)),
		countIs("sys.check_constraints WHERE parent_object_id = "+id, len(t.Checks)),
	}
	for _, column := range t.Columns {
		state, err := columnState(id, column)
		if err != nil {
			return "", fmt.Errorf("table type %s: %w", qualified(t.Name), err)
		}
		conditions = append(conditions, state)
	}
	for _, key := range t.Keys {
		key.Constraint = true
		conditions = append(conditions, indexState(id, key))
	}
	for _, check := range t.Checks {
		conditions = append(conditions, checkState(id, migrate.Check{Expression: check}))
	}
	return "(" + strings.Join(conditions, " AND ") + ")", nil
}

const sequenceTypeSQL = "CASE WHEN TYPE_NAME(q.user_type_id) IN (N'decimal', N'numeric') THEN TYPE_NAME(q.user_type_id) + N'(' + CAST(q.precision AS nvarchar(10)) + N',' + CAST(q.scale AS nvarchar(10)) + N')' ELSE TYPE_NAME(q.user_type_id) END"

// sequenceState matches a sequence as the inspector reads it: type, bounds as
// nvarchar(60) text, cycling and cache.
func sequenceState(q migrate.Sequence) string {
	conditions := []string{
		"q.object_id = OBJECT_ID(" + literal(qualified(q.Name)) + ", N'SO')", sequenceTypeSQL + " = " + literal(q.Type),
		"CAST(q.start_value AS nvarchar(60)) = " + literal(q.Start), "CAST(q.increment AS nvarchar(60)) = " + literal(q.Increment),
		"CAST(q.minimum_value AS nvarchar(60)) = " + literal(q.Minimum), "CAST(q.maximum_value AS nvarchar(60)) = " + literal(q.Maximum),
		"q.is_cycling = " + bit(q.Cycle), "q.is_cached = " + bit(!q.NoCache),
	}
	if !q.NoCache {
		conditions = append(conditions, "ISNULL(q.cache_size, 0) = "+strconv.FormatInt(q.CacheSize, 10))
	}
	return "EXISTS (SELECT 1 FROM sys.sequences AS q WHERE " + strings.Join(conditions, " AND ") + ")"
}

// columnTypeSQL renders a sys.columns type in FormatType's canonical spelling.
const columnTypeSQL = "CASE WHEN TYPE_NAME(c.user_type_id) IN (N'nchar', N'nvarchar') THEN TYPE_NAME(c.user_type_id) + N'(' + CASE WHEN c.max_length = -1 THEN N'max' ELSE CAST(c.max_length / 2 AS nvarchar(10)) END + N')'" +
	" WHEN TYPE_NAME(c.user_type_id) IN (N'char', N'varchar', N'binary', N'varbinary') THEN TYPE_NAME(c.user_type_id) + N'(' + CASE WHEN c.max_length = -1 THEN N'max' ELSE CAST(c.max_length AS nvarchar(10)) END + N')'" +
	" WHEN TYPE_NAME(c.user_type_id) IN (N'decimal', N'numeric') THEN TYPE_NAME(c.user_type_id) + N'(' + CAST(c.precision AS nvarchar(10)) + N',' + CAST(c.scale AS nvarchar(10)) + N')'" +
	" WHEN TYPE_NAME(c.user_type_id) IN (N'time', N'datetime2', N'datetimeoffset') THEN TYPE_NAME(c.user_type_id) + N'(' + CAST(c.scale AS nvarchar(10)) + N')'" +
	" WHEN TYPE_NAME(c.user_type_id) = N'timestamp' THEN N'rowversion' ELSE TYPE_NAME(c.user_type_id) END"

func columnGuard(request migrate.Request, change migrate.Change) (scriptGuard, error) {
	from, to, err := memberStates(request, change, func(t migrate.Table) []migrate.Column { return t.Columns },
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

// columnState matches a column's type, nullability, ROWGUIDCOL, identity,
// collation and default definition, never the default constraint's name. The
// default compares as the exact catalog text OBJECT_DEFINITION returns, which
// is what an inspected snapshot holds.
func columnState(objectID string, column migrate.Column) (string, error) {
	typ, err := atlas.ParseType(column.Type)
	if err != nil {
		return "", err
	}
	canonical, err := atlas.FormatType(typ)
	if err != nil {
		return "", err
	}
	conditions := []string{
		"c.object_id = " + objectID, "c.name = " + literal(column.Name), columnTypeSQL + " = " + literal(canonical),
		"c.is_nullable = " + bit(column.Nullable), "c.is_rowguidcol = " + bit(column.RowGUID), "c.is_identity = " + bit(column.Identity != nil),
	}
	if _, ok := typ.(*schema.StringType); ok {
		collation := "CAST(DATABASEPROPERTYEX(DB_NAME(), 'Collation') AS sysname)"
		if column.Collation != "" {
			collation = literal(column.Collation)
		}
		conditions = append(conditions, "c.collation_name = "+collation)
	}
	if column.Default == nil {
		conditions = append(conditions, "c.default_object_id = 0")
	} else {
		conditions = append(conditions, "OBJECT_DEFINITION(c.default_object_id) = "+literal(column.Default.Expression))
	}
	return "EXISTS (SELECT 1 FROM sys.columns AS c WHERE " + strings.Join(conditions, " AND ") + ")", nil
}

func indexGuard(request migrate.Request, change migrate.Change) (scriptGuard, error) {
	from, to, err := memberStates(request, change, func(t migrate.Table) []migrate.Index { return t.Indexes }, indexMatch(change))
	if err != nil {
		return scriptGuard{}, err
	}
	if systemName.MatchString(from.Name) {
		return scriptGuard{}, systemNamed(change, from.Name)
	}
	table := tableObjectID(change.Object)
	return scriptGuard{apply: indexState(table, from), applied: indexState(table, to)}, nil
}

const indexColumnsSQL = "ISNULL((SELECT N',' + QUOTENAME(col.name)%s FROM sys.index_columns AS ic JOIN sys.columns AS col ON col.object_id = ic.object_id AND col.column_id = ic.column_id" +
	" WHERE ic.object_id = i.object_id AND ic.index_id = i.index_id AND ic.is_included_column = %d ORDER BY %s FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), N'')"

// indexState matches an index's name (unless SQL Server named it), kind,
// uniqueness, clustering, fill factor, key and included columns, and its filter
// as the exact catalog text.
func indexState(objectID string, index migrate.Index) string {
	keys, included := "", ""
	for _, column := range index.Columns {
		keys += "," + quote(column.Name) + map[bool]string{true: " DESC"}[column.Descending]
	}
	names := slices.Clone(index.Included)
	slices.Sort(names)
	for _, name := range names {
		included += "," + quote(name)
	}
	fillFactor := "i.fill_factor IN (0, 100)"
	if index.FillFactor != 0 {
		fillFactor = "i.fill_factor = " + strconv.Itoa(index.FillFactor)
	}
	filter := "i.filter_definition IS NULL"
	if index.Filter != "" {
		filter = "i.filter_definition = " + literal(index.Filter)
	}
	conditions := []string{"i.object_id = " + objectID}
	if index.Name != "" && !systemName.MatchString(index.Name) {
		conditions = append(conditions, "i.name = "+literal(index.Name))
	}
	conditions = append(conditions,
		"i.is_primary_key = "+bit(index.Primary), "i.is_unique_constraint = "+bit(index.Constraint && !index.Primary),
		"i.is_unique = "+bit(index.Unique || index.Primary), "i.type = "+map[bool]string{true: "1", false: "2"}[index.Clustered], fillFactor,
		fmt.Sprintf(indexColumnsSQL, " + CASE WHEN ic.is_descending_key = 1 THEN N' DESC' ELSE N'' END", 0, "ic.key_ordinal")+" = "+literal(keys),
		fmt.Sprintf(indexColumnsSQL, "", 1, "col.name COLLATE Latin1_General_BIN2")+" = "+literal(included), filter,
	)
	return "EXISTS (SELECT 1 FROM sys.indexes AS i WHERE " + strings.Join(conditions, " AND ") + ")"
}

func foreignKeyGuard(request migrate.Request, change migrate.Change) (scriptGuard, error) {
	from, to, err := memberStates(request, change, func(t migrate.Table) []migrate.ForeignKey { return t.ForeignKeys },
		func(f migrate.ForeignKey) bool { return f.Name == change.Member })
	if err != nil {
		return scriptGuard{}, err
	}
	return scriptGuard{apply: foreignKeyState(change.Object, from), applied: foreignKeyState(change.Object, to)}, nil
}

const foreignKeyColumnsSQL = "ISNULL((SELECT N',' + QUOTENAME(pc.name) + N'>' + QUOTENAME(rc.name) FROM sys.foreign_key_columns AS k" +
	" JOIN sys.columns AS pc ON pc.object_id = k.parent_object_id AND pc.column_id = k.parent_column_id" +
	" JOIN sys.columns AS rc ON rc.object_id = k.referenced_object_id AND rc.column_id = k.referenced_column_id" +
	" WHERE k.constraint_object_id = f.object_id ORDER BY k.constraint_column_id FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), N'')"

func foreignKeyState(table migrate.Name, key migrate.ForeignKey) string {
	columns := ""
	for i, column := range key.Columns {
		columns += "," + quote(column) + ">" + quote(key.ReferencedColumns[i])
	}
	conditions := []string{"f.parent_object_id = " + tableObjectID(table)}
	if !systemName.MatchString(key.Name) {
		conditions = append(conditions, "f.name = "+literal(key.Name))
	}
	conditions = append(conditions,
		"f.referenced_object_id = "+tableObjectID(key.References),
		"f.delete_referential_action_desc = "+literal(referentialAction(key.OnDelete)),
		"f.update_referential_action_desc = "+literal(referentialAction(key.OnUpdate)),
		foreignKeyColumnsSQL+" = "+literal(columns),
	)
	return "EXISTS (SELECT 1 FROM sys.foreign_keys AS f WHERE " + strings.Join(conditions, " AND ") + ")"
}

func referentialAction(action string) string {
	if action == "" {
		return "NO_ACTION"
	}
	return strings.ReplaceAll(strings.ToUpper(action), " ", "_")
}

// checkGuard matches the baseline's and the desired constraint's definitions
// exactly as sys.check_constraints stores them.
func checkGuard(request migrate.Request, change migrate.Change) (scriptGuard, error) {
	from, to, err := memberStates(request, change, func(t migrate.Table) []migrate.Check { return t.Checks },
		func(c migrate.Check) bool { return c.Name == change.Member })
	if err != nil {
		return scriptGuard{}, err
	}
	table := tableObjectID(change.Object)
	return scriptGuard{apply: checkState(table, from), applied: checkState(table, to)}, nil
}

// checkState matches a check's name (unless SQL Server named it) and its
// definition as the exact catalog text.
func checkState(objectID string, check migrate.Check) string {
	conditions := []string{"k.parent_object_id = " + objectID}
	if check.Name != "" && !systemName.MatchString(check.Name) {
		conditions = append(conditions, "k.name = "+literal(check.Name))
	}
	conditions = append(conditions, "k.definition = "+literal(check.Expression))
	return "EXISTS (SELECT 1 FROM sys.check_constraints AS k WHERE " + strings.Join(conditions, " AND ") + ")"
}

func bit(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func quote(identifier string) string { return "[" + strings.ReplaceAll(identifier, "]", "]]") + "]" }

func qualified(name migrate.Name) string { return quote(name.Schema) + "." + quote(name.Name) }

func literal(text string) string { return "N'" + strings.ReplaceAll(text, "'", "''") + "'" }
