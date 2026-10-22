package snapshot

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/flanksource/sqlglot-go/migrate"
)

func readCollations(ctx context.Context, db Querier, c *catalog) error {
	return visit(ctx, db, schemaQuery{SQL: `SELECT o.name, c.name, c.collation_name
		FROM sys.columns c JOIN sys.objects o ON o.object_id=c.object_id
		JOIN sys.schemas s ON s.schema_id=o.schema_id
		WHERE s.name=@p1 AND o.type IN ('U','V') AND c.collation_name IS NOT NULL
		ORDER BY o.name, c.column_id`, Schema: c.schema}, func(rows *sql.Rows) error {
		var table, column, collation string
		if err := rows.Scan(&table, &column, &collation); err != nil {
			return err
		}
		c.collations[[2]string{table, column}] = collation
		return nil
	})
}

func readTriggers(ctx context.Context, db Querier, c *catalog) error {
	return visit(ctx, db, schemaQuery{SQL: `SELECT SCHEMA_NAME(trigger_object.schema_id), tr.name, s.name, o.name, tr.is_disabled, m.definition
		FROM sys.triggers tr JOIN sys.objects o ON o.object_id=tr.parent_id
		JOIN sys.objects trigger_object ON trigger_object.object_id=tr.object_id
		JOIN sys.schemas s ON s.schema_id=o.schema_id
		LEFT JOIN sys.sql_modules m ON m.object_id=tr.object_id
		WHERE s.name=@p1 AND tr.parent_class=1 ORDER BY tr.name`, Schema: c.schema}, func(rows *sql.Rows) error {
		var d migrate.Definition
		var parent migrate.Name
		var definition sql.NullString
		if err := rows.Scan(&d.Name.Schema, &d.Name.Name, &parent.Schema, &parent.Name, &d.Disabled, &definition); err != nil {
			return err
		}
		if !definition.Valid || definition.String == "" {
			return fmt.Errorf("trigger %v has no readable definition", d.Name)
		}
		d.Kind, d.SQL, d.Parent = "TRIGGER", definition.String, &parent
		c.modules = append(c.modules, d)
		return nil
	})
}

func readDependencies(ctx context.Context, db Querier, c *catalog) error {
	return visit(ctx, db, schemaQuery{SQL: `SELECT DISTINCT o.name, d.referenced_server_name, d.referenced_database_name,
		COALESCE(SCHEMA_NAME(r.schema_id), d.referenced_schema_name), d.referenced_entity_name, d.referenced_id
		FROM sys.sql_expression_dependencies d JOIN sys.objects o ON o.object_id=d.referencing_id
		JOIN sys.schemas s ON s.schema_id=o.schema_id LEFT JOIN sys.objects r ON r.object_id=d.referenced_id
		WHERE s.name=@p1 AND d.referenced_class=1 AND o.type IN ('V','P','FN','IF','TF','TR')
		ORDER BY 1, 4, 5`, Schema: c.schema}, func(rows *sql.Rows) error {
		var owner, entity string
		var server, database, schema sql.NullString
		var id sql.NullInt64
		if err := rows.Scan(&owner, &server, &database, &schema, &entity, &id); err != nil {
			return err
		}
		if server.Valid || database.Valid || !schema.Valid || !id.Valid {
			return fmt.Errorf("module %s.%s has an external or unresolved dependency %q", c.schema, owner, entity)
		}
		name := migrate.Name{Schema: schema.String, Name: entity}
		if !slices.Contains(c.dependencies[owner], name) {
			c.dependencies[owner] = append(c.dependencies[owner], name)
		}
		return nil
	})
}

type schemaQuery struct {
	SQL, Schema string
}

func visit(ctx context.Context, db Querier, query schemaQuery, scan func(*sql.Rows) error) (err error) {
	rows, err := db.QueryContext(ctx, query.SQL, query.Schema)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := rows.Close(); err == nil {
			err = closeErr
		}
	}()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Every flag below changes DDL semantics that the native model cannot preserve.
const unsupportedQuery = `SELECT object_name, feature FROM (
	SELECT t.name AS object_name, 'table storage or temporal settings' AS feature
	FROM sys.tables t JOIN sys.schemas s ON s.schema_id=t.schema_id
	WHERE s.name=@p1 AND (t.temporal_type<>0 OR t.is_memory_optimized=1 OR t.is_filetable=1 OR COALESCE(t.filestream_data_space_id,0)<>0)
	UNION ALL
	SELECT o.name, 'special column settings' FROM sys.columns c JOIN sys.objects o ON o.object_id=c.object_id
	JOIN sys.schemas s ON s.schema_id=o.schema_id
	WHERE s.name=@p1 AND o.type='U' AND (c.is_sparse=1 OR c.is_column_set=1 OR c.is_filestream=1 OR c.generated_always_type<>0 OR c.is_rowguidcol=1 OR c.encryption_type IS NOT NULL OR c.is_hidden=1 OR c.rule_object_id<>0)
	UNION ALL
	SELECT t.name, 'index storage, state or compression' FROM sys.indexes i JOIN sys.tables t ON t.object_id=i.object_id
	JOIN sys.schemas s ON s.schema_id=t.schema_id LEFT JOIN sys.data_spaces ds ON ds.data_space_id=i.data_space_id
	WHERE s.name=@p1 AND (i.is_disabled=1 OR i.is_hypothetical=1 OR i.type NOT IN (0,1,2) OR ds.type='PS'
	OR i.fill_factor<>0 OR i.is_padded=1 OR i.ignore_dup_key=1 OR i.allow_row_locks=0 OR i.allow_page_locks=0
	OR (ds.type='FG' AND NOT EXISTS (SELECT 1 FROM sys.filegroups fg WHERE fg.data_space_id=ds.data_space_id AND fg.is_default=1))
	OR EXISTS (SELECT 1 FROM sys.partitions p WHERE p.object_id=i.object_id AND p.index_id=i.index_id AND p.data_compression<>0))
	UNION ALL
	SELECT t.name, 'untrusted or replication-sensitive check' FROM sys.check_constraints c JOIN sys.tables t ON t.object_id=c.parent_object_id
	JOIN sys.schemas s ON s.schema_id=t.schema_id WHERE s.name=@p1 AND (c.is_disabled=1 OR c.is_not_trusted=1 OR c.is_not_for_replication=1)
	UNION ALL
	SELECT t.name, 'untrusted or replication-sensitive foreign key' FROM sys.foreign_keys f JOIN sys.tables t ON t.object_id=f.parent_object_id
	JOIN sys.schemas s ON s.schema_id=t.schema_id WHERE s.name=@p1 AND (f.is_disabled=1 OR f.is_not_trusted=1 OR f.is_not_for_replication=1)
	UNION ALL
	SELECT o.name, 'module session settings' FROM sys.sql_modules m JOIN sys.objects o ON o.object_id=m.object_id
	JOIN sys.schemas s ON s.schema_id=o.schema_id WHERE s.name=@p1 AND (m.uses_ansi_nulls=0 OR m.uses_quoted_identifier=0)
	UNION ALL
	SELECT v.name, 'indexed view' FROM sys.views v JOIN sys.schemas s ON s.schema_id=v.schema_id
	WHERE s.name=@p1 AND EXISTS (SELECT 1 FROM sys.indexes i WHERE i.object_id=v.object_id AND i.index_id>0)
	UNION ALL
	SELECT o.name, 'unsupported schema object '+o.type_desc FROM sys.objects o JOIN sys.schemas s ON s.schema_id=o.schema_id
	WHERE s.name=@p1 AND o.is_ms_shipped=0 AND o.type NOT IN ('U','V','P','FN','IF','TF','TR','PK','UQ','F','C','D')
	UNION ALL
	SELECT o.name, 'dependent default, check or column expression' FROM sys.objects o JOIN sys.schemas s ON s.schema_id=o.schema_id
	JOIN sys.sql_expression_dependencies d ON d.referencing_id=o.object_id
	WHERE s.name=@p1 AND o.type IN ('U','C','D')
	AND (d.referenced_id IS NULL OR d.referenced_id<>COALESCE(NULLIF(o.parent_object_id,0),o.object_id))
) unsupported ORDER BY object_name, feature`

func refuseUnsupported(ctx context.Context, db Querier, schema string) error {
	return visit(ctx, db, schemaQuery{SQL: unsupportedQuery, Schema: schema}, func(rows *sql.Rows) error {
		var object, feature string
		if err := rows.Scan(&object, &feature); err != nil {
			return err
		}
		return fmt.Errorf("%s.%s has unsupported %s; migration snapshot is incomplete", schema, object, feature)
	})
}
