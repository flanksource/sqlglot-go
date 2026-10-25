package snapshot

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate"
)

// readIndexOptions records each index's FILLFACTOR. 0 and 100 are the same
// server default, so only 1-99 is kept.
func readIndexOptions(ctx context.Context, db Querier, c *catalog) error {
	return visit(ctx, db, schemaQuery{SQL: `SELECT t.name, i.name, i.fill_factor
		FROM sys.indexes i JOIN sys.tables t ON t.object_id=i.object_id
		JOIN sys.schemas s ON s.schema_id=t.schema_id
		WHERE s.name=@p1 AND i.name IS NOT NULL AND i.fill_factor NOT IN (0,100)
		ORDER BY t.name, i.name`, Schema: c.schema}, func(rows *sql.Rows) error {
		var table, index string
		var fillFactor int
		if err := rows.Scan(&table, &index, &fillFactor); err != nil {
			return err
		}
		c.fillFactors[[2]string{table, index}] = fillFactor
		return nil
	})
}

// readSystemNamedDefaults records defaults whose constraint name SQL Server
// generated, which differs in every database.
func readSystemNamedDefaults(ctx context.Context, db Querier, c *catalog) error {
	return visit(ctx, db, schemaQuery{SQL: `SELECT o.name, col.name
		FROM sys.default_constraints dc JOIN sys.objects o ON o.object_id=dc.parent_object_id
		JOIN sys.columns col ON col.object_id=dc.parent_object_id AND col.column_id=dc.parent_column_id
		JOIN sys.schemas s ON s.schema_id=o.schema_id
		WHERE s.name=@p1 AND o.type='U' AND dc.is_system_named=1
		ORDER BY o.name`, Schema: c.schema}, func(rows *sql.Rows) error {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return err
		}
		c.systemNamedDefaults[[2]string{table, column}] = true
		return nil
	})
}

func readRowGUIDs(ctx context.Context, db Querier, c *catalog) error {
	return visit(ctx, db, schemaQuery{SQL: `SELECT o.name, c.name
		FROM sys.columns c JOIN sys.objects o ON o.object_id=c.object_id
		JOIN sys.schemas s ON s.schema_id=o.schema_id
		WHERE s.name=@p1 AND o.type='U' AND c.is_rowguidcol=1
		ORDER BY o.name`, Schema: c.schema}, func(rows *sql.Rows) error {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return err
		}
		c.rowGUIDs[[2]string{table, column}] = true
		return nil
	})
}

// readModuleSettings records modules created under QUOTED_IDENTIFIER OFF.
// ANSI_NULLS OFF stays unsupported and is refused by refuseUnsupported.
func readModuleSettings(ctx context.Context, db Querier, c *catalog) error {
	return visit(ctx, db, schemaQuery{SQL: `SELECT o.name
		FROM sys.sql_modules m JOIN sys.objects o ON o.object_id=m.object_id
		JOIN sys.schemas s ON s.schema_id=o.schema_id
		WHERE s.name=@p1 AND m.uses_quoted_identifier=0
		ORDER BY o.name`, Schema: c.schema}, func(rows *sql.Rows) error {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		c.quotedIdentifierOff[name] = true
		return nil
	})
}

func readSequences(ctx context.Context, db Querier, c *catalog) error {
	return visit(ctx, db, schemaQuery{SQL: `SELECT q.name, t.name, t.is_user_defined, q.precision, q.scale,
		CAST(q.start_value AS nvarchar(60)), CAST(q.increment AS nvarchar(60)),
		CAST(q.minimum_value AS nvarchar(60)), CAST(q.maximum_value AS nvarchar(60)),
		q.is_cycling, q.is_cached, q.cache_size
		FROM sys.sequences q JOIN sys.schemas s ON s.schema_id=q.schema_id
		JOIN sys.types t ON t.user_type_id=q.user_type_id
		WHERE s.name=@p1 ORDER BY q.name`, Schema: c.schema}, func(rows *sql.Rows) error {
		q := migrate.Sequence{Name: migrate.Name{Schema: c.schema}}
		var typeName string
		var userDefined, cached bool
		var precision, scale int
		var cacheSize sql.NullInt64
		if err := rows.Scan(&q.Name.Name, &typeName, &userDefined, &precision, &scale, &q.Start, &q.Increment, &q.Minimum, &q.Maximum, &q.Cycle, &cached, &cacheSize); err != nil {
			return err
		}
		if userDefined {
			return fmt.Errorf("sequence %s.%s uses unsupported user-defined type %s", c.schema, q.Name.Name, typeName)
		}
		q.Type = typeName
		if strings.EqualFold(typeName, "decimal") || strings.EqualFold(typeName, "numeric") {
			q.Type = fmt.Sprintf("%s(%d,%d)", typeName, precision, scale)
		}
		q.NoCache = !cached
		if cached && cacheSize.Valid {
			q.CacheSize = cacheSize.Int64
		}
		c.sequences = append(c.sequences, q)
		return nil
	})
}
