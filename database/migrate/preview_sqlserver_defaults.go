package migrate

import (
	"context"
	"database/sql"
	"fmt"

	"ariga.io/atlas/sql/schema"
	native "github.com/flanksource/sqlglot-go/migrate"
)

func resolveSQLServerDefaults(ctx context.Context, db *sql.DB, desired *schema.Realm, current native.Schema) error {
	var collation string
	if err := db.QueryRowContext(ctx, "SELECT CONVERT(nvarchar(128), DATABASEPROPERTYEX(DB_NAME(), 'Collation'))").Scan(&collation); err != nil {
		return fmt.Errorf("inspect SQL Server database collation: %w", err)
	}
	if collation == "" {
		return fmt.Errorf("SQL Server database collation is empty")
	}
	for _, s := range desired.Schemas {
		for _, table := range s.Tables {
			resolveSQLServerTableDefaults(table, current, collation)
		}
	}
	return nil
}

func resolveSQLServerTableDefaults(table *schema.Table, current native.Schema, collation string) {
	var before native.Table
	for _, candidate := range current.Tables {
		if candidate.Name == (native.Name{Schema: table.Schema.Name, Name: table.Name}) {
			before = candidate
			break
		}
	}
	if table.PrimaryKey != nil && table.PrimaryKey.Name == "" {
		for _, index := range before.Indexes {
			if index.Primary {
				table.PrimaryKey.Name = index.Name
			}
		}
	}
	for _, column := range table.Columns {
		if _, ok := column.Type.Type.(*schema.StringType); !ok {
			continue
		}
		effective := collation
		for _, old := range before.Columns {
			if old.Name == column.Name && old.Collation != "" {
				effective = old.Collation
				break
			}
		}
		column.SetCollation(effective)
	}
}
