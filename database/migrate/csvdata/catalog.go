package csvdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type column struct {
	Name      string
	Type      string
	Collation string
	Length    int
	Precision int
	Scale     int
	Nullable  bool
	Identity  bool
	Generated bool
	Default   string
	Primary   int
}

type relationship struct {
	Name         string
	Child        string
	Parent       string
	DeleteAction int
	UpdateAction int
	Columns      string
}

type metadata struct {
	Object      int
	Name        string
	Columns     []column
	Relations   []relationship
	Constraints []string
	Triggers    []string
}

func tableMetadata(ctx context.Context, tx *sql.Tx, name string) (_ metadata, returnErr error) {
	var result metadata
	schema, table, _ := strings.Cut(name, ".")
	err := tx.QueryRowContext(ctx, `SELECT t.object_id, s.name + N'.' + t.name
FROM sys.tables t JOIN sys.schemas s ON s.schema_id=t.schema_id
WHERE s.name=@p1 AND t.name=@p2 AND t.temporal_type<>1 AND t.is_memory_optimized=0`, schema, table).Scan(&result.Object, &result.Name)
	if err != nil {
		return result, fmt.Errorf("resolve writable CSV table %s: %w", name, err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.name, TYPE_NAME(c.system_type_id), COALESCE(c.collation_name,''),
c.max_length,c.precision,c.scale,c.is_nullable,c.is_identity,
CAST(CASE WHEN c.is_computed=1 OR c.generated_always_type<>0 OR c.system_type_id=189 OR c.is_hidden=1 THEN 1 ELSE 0 END AS bit),
COALESCE(d.definition,''),COALESCE(ic.key_ordinal,0)
FROM sys.columns c
LEFT JOIN sys.default_constraints d ON d.object_id=c.default_object_id
LEFT JOIN sys.indexes i ON i.object_id=c.object_id AND i.is_primary_key=1
LEFT JOIN sys.index_columns ic ON ic.object_id=i.object_id AND ic.index_id=i.index_id AND ic.column_id=c.column_id
WHERE c.object_id=@p1 ORDER BY c.column_id`, result.Object)
	if err != nil {
		return result, err
	}
	defer closeRows(rows, &returnErr)
	for rows.Next() {
		var c column
		if err := rows.Scan(&c.Name, &c.Type, &c.Collation, &c.Length, &c.Precision, &c.Scale, &c.Nullable, &c.Identity, &c.Generated, &c.Default, &c.Primary); err != nil {
			return result, err
		}
		result.Columns = append(result.Columns, c)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if err := inspectRelationships(ctx, tx, &result); err != nil {
		return result, err
	}
	return result, inspectConstraints(ctx, tx, &result)
}

func inspectRelationships(ctx context.Context, tx *sql.Tx, result *metadata) (returnErr error) {
	rows, err := tx.QueryContext(ctx, `SELECT f.name, OBJECT_SCHEMA_NAME(f.parent_object_id)+N'.'+OBJECT_NAME(f.parent_object_id),
OBJECT_SCHEMA_NAME(f.referenced_object_id)+N'.'+OBJECT_NAME(f.referenced_object_id),
f.delete_referential_action, f.update_referential_action,
STRING_AGG(CONVERT(nvarchar(max),CONCAT(COL_NAME(fc.parent_object_id,fc.parent_column_id),N'=',COL_NAME(fc.referenced_object_id,fc.referenced_column_id))),N',')
WITHIN GROUP (ORDER BY fc.constraint_column_id)
FROM sys.foreign_keys f JOIN sys.foreign_key_columns fc ON fc.constraint_object_id=f.object_id
WHERE f.is_disabled=0 AND (f.parent_object_id=@p1 OR f.referenced_object_id=@p1)
GROUP BY f.object_id,f.name,f.parent_object_id,f.referenced_object_id,f.delete_referential_action,f.update_referential_action
ORDER BY f.object_id`, result.Object)
	if err != nil {
		return err
	}
	defer closeRows(rows, &returnErr)
	for rows.Next() {
		var relation relationship
		if err := rows.Scan(&relation.Name, &relation.Child, &relation.Parent, &relation.DeleteAction, &relation.UpdateAction, &relation.Columns); err != nil {
			return err
		}
		result.Relations = append(result.Relations, relation)
	}
	return rows.Err()
}

func inspectConstraints(ctx context.Context, tx *sql.Tx, result *metadata) (returnErr error) {
	rows, err := tx.QueryContext(ctx, `SELECT 'check',CONCAT(name,':',is_disabled,':',definition) FROM sys.check_constraints WHERE parent_object_id=@p1
UNION ALL SELECT 'trigger',name FROM sys.triggers WHERE parent_id=@p1 AND is_disabled=0
UNION ALL SELECT 'unique',CONCAT(i.name,':',i.is_disabled,':',i.filter_definition,':',ic.key_ordinal,':',c.name)
FROM sys.indexes i JOIN sys.index_columns ic ON i.object_id=ic.object_id AND i.index_id=ic.index_id
JOIN sys.columns c ON c.object_id=ic.object_id AND c.column_id=ic.column_id
WHERE i.object_id=@p1 AND i.is_unique=1 ORDER BY 1,2`, result.Object)
	if err != nil {
		return err
	}
	defer closeRows(rows, &returnErr)
	for rows.Next() {
		var kind, description string
		if err := rows.Scan(&kind, &description); err != nil {
			return err
		}
		if kind == "trigger" {
			result.Triggers = append(result.Triggers, description)
		} else {
			result.Constraints = append(result.Constraints, kind+":"+description)
		}
	}
	return rows.Err()
}

func closeRows(rows *sql.Rows, cause *error) {
	if err := rows.Close(); err != nil {
		*cause = errors.Join(*cause, err)
	}
}

func bracket(name string) string { return "[" + strings.ReplaceAll(name, "]", "]]") + "]" }

func qualified(name string) string {
	schema, table, _ := strings.Cut(name, ".")
	return bracket(schema) + "." + bracket(table)
}
