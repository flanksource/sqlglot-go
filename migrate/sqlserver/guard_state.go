package sqlserver

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
	atlas "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
)

// The functions below render a boolean T-SQL condition that holds when the
// catalog already has an object or member in exactly the given shape.

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
