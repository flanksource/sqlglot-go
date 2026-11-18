package mssqlcatalog

import (
	"context"
	"fmt"
)

// ColumnDetail supplies definition attributes used by dependency fingerprints.
type ColumnDetail struct {
	Name               string
	Collation          string
	ComputedDefinition string
	Persisted          bool
	Sparse             bool
	RowGUID            bool
}

type ColumnName struct {
	Schema string
	Table  string
	Column string
}

// ResolveColumn lets the database apply its identifier collation.
func ResolveColumn(ctx context.Context, q Queryer, name ColumnName) (ColumnName, error) {
	resolved, err := collect(ctx, q, catalogQuery{Text: `SELECT s.name,t.name,c.name
FROM sys.columns c JOIN sys.tables t ON t.object_id=c.object_id
JOIN sys.schemas s ON s.schema_id=t.schema_id
WHERE s.name=@p1 AND t.name=@p2 AND c.name=@p3`, Args: []any{name.Schema, name.Table, name.Column}}, func(rows Rows) (ColumnName, error) {
		var column ColumnName
		err := rows.Scan(&column.Schema, &column.Table, &column.Column)
		return column, err
	})
	if err != nil {
		return ColumnName{}, err
	}
	if len(resolved) != 1 {
		return ColumnName{}, fmt.Errorf("schema column %s.%s.%s is missing or not visible", name.Schema, name.Table, name.Column)
	}
	return resolved[0], nil
}

func ColumnDetails(ctx context.Context, q Queryer, scope Scope) ([]ColumnDetail, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	var b binds
	query := fmt.Sprintf(`SELECT c.name,COALESCE(c.collation_name,''),COALESCE(cc.definition,''),
COALESCE(cc.is_persisted,CAST(0 AS bit)),c.is_sparse,c.is_rowguidcol
FROM sys.columns c JOIN sys.tables t ON t.object_id=c.object_id JOIN sys.schemas s ON s.schema_id=t.schema_id
LEFT JOIN sys.computed_columns cc ON cc.object_id=c.object_id AND cc.column_id=c.column_id
WHERE s.name=%s%s ORDER BY t.name,c.column_id`, b.add(scope.Schema), b.in("t.name", scope.Names))
	return collect(ctx, q, catalogQuery{Text: query, Args: b.args}, func(rows Rows) (ColumnDetail, error) {
		var d ColumnDetail
		err := rows.Scan(&d.Name, &d.Collation, &d.ComputedDefinition, &d.Persisted, &d.Sparse, &d.RowGUID)
		return d, err
	})
}
