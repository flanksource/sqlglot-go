package mssqlcatalog

import (
	"context"
	"database/sql"
	"fmt"
)

type Module struct {
	Schema     string
	Name       string
	Kind       string
	Definition string
	ANSI       bool
	Quoted     bool
	ReturnType string
	Length     int64
	Precision  int64
	Scale      int64
	Parameters int
}

func ReadModule(ctx context.Context, q Queryer, name string) (Module, error) {
	items, err := collect(ctx, q, catalogQuery{Text: `SELECT SCHEMA_NAME(o.schema_id), o.name, RTRIM(o.type), m.definition,
m.uses_ansi_nulls, m.uses_quoted_identifier, COALESCE(TYPE_NAME(p.user_type_id), ''),
COALESCE(p.max_length, 0), COALESCE(p.precision, 0), COALESCE(p.scale, 0),
(SELECT COUNT(*) FROM sys.parameters a WHERE a.object_id=o.object_id AND a.parameter_id>0)
FROM sys.objects o LEFT JOIN sys.sql_modules m ON m.object_id=o.object_id
LEFT JOIN sys.parameters p ON p.object_id=o.object_id AND p.parameter_id=0
WHERE o.object_id=OBJECT_ID(@p1) AND o.type IN ('FN','IF','TF','P','FS','FT','PC')`, Args: []any{name}}, func(rows Rows) (Module, error) {
		var m Module
		var definition sql.NullString
		var ansi, quoted sql.NullBool
		err := rows.Scan(&m.Schema, &m.Name, &m.Kind, &definition, &ansi, &quoted, &m.ReturnType, &m.Length, &m.Precision, &m.Scale, &m.Parameters)
		if err != nil {
			return m, err
		}
		if !definition.Valid || !ansi.Valid || !quoted.Valid {
			return m, fmt.Errorf("module %s definition is missing, encrypted or unavailable", name)
		}
		m.Definition, m.ANSI, m.Quoted = definition.String, ansi.Bool, quoted.Bool
		return m, nil
	})
	if err != nil {
		return Module{}, err
	}
	if len(items) != 1 {
		return Module{}, fmt.Errorf("module %s not found or not visible", name)
	}
	return items[0], nil
}

type ModuleReference struct {
	Server, Database, Schema, Name, Kind string
	Bound                                bool
}

func ModuleReferences(ctx context.Context, q Queryer, name string) ([]ModuleReference, error) {
	return collect(ctx, q, catalogQuery{Text: `SELECT COALESCE(d.referenced_server_name,''), COALESCE(d.referenced_database_name,''),
COALESCE(SCHEMA_NAME(o.schema_id),d.referenced_schema_name,''), COALESCE(o.name,d.referenced_entity_name), COALESCE(RTRIM(o.type),''),
CAST(CASE WHEN d.referenced_id IS NULL THEN 0 ELSE 1 END AS bit)
FROM sys.sql_expression_dependencies d LEFT JOIN sys.objects o ON o.object_id=d.referenced_id
WHERE d.referencing_id=OBJECT_ID(@p1)
ORDER BY d.referenced_server_name,d.referenced_database_name,d.referenced_schema_name,d.referenced_entity_name`, Args: []any{name}}, func(rows Rows) (ModuleReference, error) {
		var r ModuleReference
		err := rows.Scan(&r.Server, &r.Database, &r.Schema, &r.Name, &r.Kind, &r.Bound)
		return r, err
	})
}
