package snapshot

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/flanksource/sqlglot-go/migrate"
	dialect "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
)

// readTableTypes reads user-defined table types. SQL Server names their
// constraints itself (randomly), so keys are ordered by creation (index_id)
// and checks by definition, never by name.
func readTableTypes(ctx context.Context, db Querier, c *catalog) error {
	if err := readTableTypeColumns(ctx, db, c); err != nil {
		return err
	}
	if err := readTableTypeKeys(ctx, db, c); err != nil {
		return err
	}
	return visit(ctx, db, schemaQuery{SQL: `SELECT tt.name, cc.definition
		FROM sys.check_constraints cc JOIN sys.table_types tt ON tt.type_table_object_id=cc.parent_object_id
		JOIN sys.schemas s ON s.schema_id=tt.schema_id
		WHERE s.name=@p1 ORDER BY tt.name, cc.definition`, Schema: c.schema}, func(rows *sql.Rows) error {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			return err
		}
		t, err := c.tableType(name)
		if err != nil {
			return err
		}
		t.Checks = append(t.Checks, definition)
		return nil
	})
}

func (c *catalog) tableType(name string) (*migrate.TableType, error) {
	i := slices.IndexFunc(c.tableTypes, func(t migrate.TableType) bool { return t.Name.Name == name })
	if i < 0 {
		return nil, fmt.Errorf("table type %s.%s has constraints but no columns", c.schema, name)
	}
	return &c.tableTypes[i], nil
}

func readTableTypeColumns(ctx context.Context, db Querier, c *catalog) error {
	return visit(ctx, db, schemaQuery{SQL: `SELECT tt.name, col.name, ty.name, ty.is_user_defined, col.max_length, col.precision, col.scale,
		col.is_nullable, col.is_identity, CAST(idc.seed_value AS bigint), CAST(idc.increment_value AS bigint), dc.definition, col.collation_name
		FROM sys.table_types tt JOIN sys.schemas s ON s.schema_id=tt.schema_id
		JOIN sys.columns col ON col.object_id=tt.type_table_object_id
		JOIN sys.types ty ON ty.user_type_id=col.user_type_id
		LEFT JOIN sys.identity_columns idc ON idc.object_id=col.object_id AND idc.column_id=col.column_id
		LEFT JOIN sys.default_constraints dc ON dc.parent_object_id=col.object_id AND dc.parent_column_id=col.column_id
		WHERE s.name=@p1 ORDER BY tt.name, col.column_id`, Schema: c.schema}, func(rows *sql.Rows) error {
		var typeName, columnName, dataType string
		var userDefined, nullable, identity bool
		var maxLength, precision, scale int
		var seed, increment sql.NullInt64
		var defaultValue, collation sql.NullString
		if err := rows.Scan(&typeName, &columnName, &dataType, &userDefined, &maxLength, &precision, &scale, &nullable, &identity, &seed, &increment, &defaultValue, &collation); err != nil {
			return err
		}
		if userDefined {
			return fmt.Errorf("table type %s.%s column %s uses unsupported user-defined type %s", c.schema, typeName, columnName, dataType)
		}
		typ, err := dialect.CatalogType(dialect.CatalogColumn{Name: columnName, DataType: dataType, MaxLength: &maxLength, Precision: &precision, Scale: &scale})
		if err != nil {
			return fmt.Errorf("table type %s.%s column %s: %w", c.schema, typeName, columnName, err)
		}
		column := migrate.Column{Name: columnName, Nullable: nullable, Collation: collation.String}
		if column.Type, err = dialect.FormatType(typ); err != nil {
			return err
		}
		if identity {
			if !seed.Valid || !increment.Valid {
				return fmt.Errorf("table type %s.%s column %s: identity seed and increment are missing", c.schema, typeName, columnName)
			}
			column.Identity = &migrate.Identity{Seed: seed.Int64, Increment: increment.Int64}
		}
		if defaultValue.Valid {
			column.Default = &migrate.Default{Expression: defaultValue.String}
		}
		if n := len(c.tableTypes); n == 0 || c.tableTypes[n-1].Name.Name != typeName {
			c.tableTypes = append(c.tableTypes, migrate.TableType{Name: migrate.Name{Schema: c.schema, Name: typeName}})
		}
		last := &c.tableTypes[len(c.tableTypes)-1]
		last.Columns = append(last.Columns, column)
		return nil
	})
}

func readTableTypeKeys(ctx context.Context, db Querier, c *catalog) error {
	keys := map[[2]any]int{}
	return visit(ctx, db, schemaQuery{SQL: `SELECT tt.name, i.index_id, i.is_primary_key, i.is_unique, i.type_desc, col.name, ic.is_descending_key, ic.is_included_column
		FROM sys.table_types tt JOIN sys.schemas s ON s.schema_id=tt.schema_id
		JOIN sys.indexes i ON i.object_id=tt.type_table_object_id
		JOIN sys.index_columns ic ON ic.object_id=i.object_id AND ic.index_id=i.index_id
		JOIN sys.columns col ON col.object_id=ic.object_id AND col.column_id=ic.column_id
		WHERE s.name=@p1 AND i.index_id>0 ORDER BY tt.name, i.index_id, ic.key_ordinal`, Schema: c.schema}, func(rows *sql.Rows) error {
		var typeName, typeDesc, column string
		var indexID int
		var primary, unique, descending, included bool
		if err := rows.Scan(&typeName, &indexID, &primary, &unique, &typeDesc, &column, &descending, &included); err != nil {
			return err
		}
		if !unique || included || typeDesc != "CLUSTERED" && typeDesc != "NONCLUSTERED" {
			return fmt.Errorf("table type %s.%s has an unsupported non-unique, included-column or %s index", c.schema, typeName, typeDesc)
		}
		t, err := c.tableType(typeName)
		if err != nil {
			return err
		}
		at, ok := keys[[2]any{typeName, indexID}]
		if !ok {
			t.Keys = append(t.Keys, migrate.Index{Primary: primary, Unique: !primary, Clustered: typeDesc == "CLUSTERED"})
			at = len(t.Keys) - 1
			keys[[2]any{typeName, indexID}] = at
		}
		t.Keys[at].Columns = append(t.Keys[at].Columns, migrate.IndexColumn{Name: column, Descending: descending})
		return nil
	})
}
