package mssqlcatalog

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// moduleBatch bounds the names one module query binds, keeping it far below
// SQL Server's 2100-parameter limit.
const moduleBatch = 1000

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
	// Unavailable marks a module whose definition is missing, encrypted or
	// otherwise withheld by the catalog; Definition, ANSI and Quoted are unset.
	Unavailable bool
}

// ReadModules reads the routines names resolve to with OBJECT_ID, keyed by the
// requested name. A name that resolves to no visible routine is absent.
func ReadModules(ctx context.Context, q Queryer, names []string) (map[string]Module, error) {
	if err := validateNames(names); err != nil {
		return nil, err
	}
	out := map[string]Module{}
	for chunk := range slices.Chunk(distinct(names), moduleBatch) {
		var b binds
		items, err := collect(ctx, q, catalogQuery{Text: `SELECT v.name, SCHEMA_NAME(o.schema_id), o.name, RTRIM(o.type), m.definition,
m.uses_ansi_nulls, m.uses_quoted_identifier, COALESCE(TYPE_NAME(p.user_type_id), ''),
COALESCE(p.max_length, 0), COALESCE(p.precision, 0), COALESCE(p.scale, 0),
(SELECT COUNT(*) FROM sys.parameters a WHERE a.object_id=o.object_id AND a.parameter_id>0)
FROM ` + b.values(chunk) + ` JOIN sys.objects o ON o.object_id=OBJECT_ID(v.name)
LEFT JOIN sys.sql_modules m ON m.object_id=o.object_id
LEFT JOIN sys.parameters p ON p.object_id=o.object_id AND p.parameter_id=0
WHERE o.type IN ('FN','IF','TF','P','FS','FT','PC')`, Args: b.args}, scanModule)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if _, duplicate := out[item.requested]; duplicate {
				return nil, fmt.Errorf("module %s matched more than one catalog row", item.requested)
			}
			out[item.requested] = item.Module
		}
	}
	return out, nil
}

type requestedModule struct {
	requested string
	Module
}

func scanModule(rows Rows) (requestedModule, error) {
	var m requestedModule
	var definition sql.NullString
	var ansi, quoted sql.NullBool
	err := rows.Scan(&m.requested, &m.Schema, &m.Name, &m.Kind, &definition, &ansi, &quoted, &m.ReturnType, &m.Length, &m.Precision, &m.Scale, &m.Parameters)
	if !definition.Valid || !ansi.Valid || !quoted.Valid {
		m.Unavailable = true
		return m, err
	}
	m.Definition, m.ANSI, m.Quoted = definition.String, ansi.Bool, quoted.Bool
	return m, err
}

type ModuleReference struct {
	Server, Database, Schema, Name, Kind string
	Bound                                bool
}

// ReadModuleReferences reads what each named module references, keyed by the
// requested name, each list in catalog order. A module that references
// nothing is absent.
func ReadModuleReferences(ctx context.Context, q Queryer, names []string) (map[string][]ModuleReference, error) {
	if err := validateNames(names); err != nil {
		return nil, err
	}
	out := map[string][]ModuleReference{}
	for chunk := range slices.Chunk(distinct(names), moduleBatch) {
		var b binds
		type requestedReference struct {
			requested string
			ModuleReference
		}
		items, err := collect(ctx, q, catalogQuery{Text: `SELECT v.name, COALESCE(d.referenced_server_name,''), COALESCE(d.referenced_database_name,''),
COALESCE(SCHEMA_NAME(o.schema_id),d.referenced_schema_name,''), COALESCE(o.name,d.referenced_entity_name), COALESCE(RTRIM(o.type),''),
CAST(CASE WHEN d.referenced_id IS NULL THEN 0 ELSE 1 END AS bit)
FROM ` + b.values(chunk) + ` JOIN sys.sql_expression_dependencies d ON d.referencing_id=OBJECT_ID(v.name)
LEFT JOIN sys.objects o ON o.object_id=d.referenced_id
ORDER BY v.name,d.referenced_server_name,d.referenced_database_name,d.referenced_schema_name,d.referenced_entity_name`, Args: b.args}, func(rows Rows) (requestedReference, error) {
			var r requestedReference
			err := rows.Scan(&r.requested, &r.Server, &r.Database, &r.Schema, &r.Name, &r.Kind, &r.Bound)
			return r, err
		})
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			out[item.requested] = append(out[item.requested], item.ModuleReference)
		}
	}
	return out, nil
}

// values renders a one-column derived table `(VALUES (@pX), ...) v(name)`,
// the compatibility-level-100 way to join a name list.
func (b *binds) values(names []string) string {
	rows := make([]string, len(names))
	for i, name := range names {
		rows[i] = "(" + b.add(name) + ")"
	}
	return "(VALUES " + strings.Join(rows, ", ") + ") v(name)"
}

func distinct(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}
