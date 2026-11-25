package snapshot

import (
	"context"
	"database/sql"

	"github.com/flanksource/sqlglot-go/migrate"
)

// refusalQuery lists what the native model cannot hold, one row per object or
// member. An empty member refuses the whole object. It is the per-object form
// of unsupportedQuery: index options that a rebuild can read back (fill factor,
// compression, locks, filegroup, NOT FOR REPLICATION) are not refused here.
// The name filter wraps the union, which SQL Server pushes into its branches.
const refusalQuery = `SELECT object_name, member_name, feature FROM (
	SELECT t.name AS object_name, '' AS member_name, 'table storage or temporal settings' AS feature
	FROM sys.tables t JOIN sys.schemas s ON s.schema_id=t.schema_id
	WHERE s.name=@p1 AND (t.temporal_type<>0 OR t.is_memory_optimized=1 OR t.is_filetable=1 OR COALESCE(t.filestream_data_space_id,0)<>0)
	UNION ALL
	SELECT o.name, '', 'special column settings on column '+c.name COLLATE DATABASE_DEFAULT FROM sys.columns c JOIN sys.objects o ON o.object_id=c.object_id
	JOIN sys.schemas s ON s.schema_id=o.schema_id
	WHERE s.name=@p1 AND o.type='U' AND (c.is_sparse=1 OR c.is_column_set=1 OR c.is_filestream=1 OR c.generated_always_type<>0 OR c.encryption_type IS NOT NULL OR c.is_hidden=1 OR c.rule_object_id<>0)
	UNION ALL
	SELECT t.name, i.name, 'index '+STUFF(
		CASE WHEN i.is_disabled=1 THEN ', is disabled' ELSE '' END +
		CASE WHEN i.is_hypothetical=1 THEN ', is hypothetical' ELSE '' END +
		CASE WHEN i.type NOT IN (1,2) THEN ', has unsupported type '+i.type_desc COLLATE DATABASE_DEFAULT ELSE '' END +
		CASE WHEN ds.type='PS' THEN ', is partitioned' ELSE '' END, 1, 2, '')
	FROM sys.indexes i JOIN sys.tables t ON t.object_id=i.object_id JOIN sys.schemas s ON s.schema_id=t.schema_id
	LEFT JOIN sys.data_spaces ds ON ds.data_space_id=i.data_space_id
	WHERE s.name=@p1 AND i.name IS NOT NULL AND (i.is_disabled=1 OR i.is_hypothetical=1 OR i.type NOT IN (1,2) OR ds.type='PS')
	UNION ALL
	SELECT t.name, c.name, 'check constraint is not trusted' FROM sys.check_constraints c JOIN sys.tables t ON t.object_id=c.parent_object_id
	JOIN sys.schemas s ON s.schema_id=t.schema_id WHERE s.name=@p1 AND c.is_not_trusted=1 AND c.is_disabled=0
	UNION ALL
	SELECT t.name, f.name, CASE WHEN f.is_disabled=1 THEN 'foreign key is disabled' ELSE 'foreign key is not trusted' END
	FROM sys.foreign_keys f JOIN sys.tables t ON t.object_id=f.parent_object_id
	JOIN sys.schemas s ON s.schema_id=t.schema_id WHERE s.name=@p1 AND (f.is_disabled=1 OR f.is_not_trusted=1)
	UNION ALL
	SELECT o.name, '', 'module created with ANSI_NULLS OFF' FROM sys.sql_modules m JOIN sys.objects o ON o.object_id=m.object_id
	JOIN sys.schemas s ON s.schema_id=o.schema_id WHERE s.name=@p1 AND m.uses_ansi_nulls=0
	UNION ALL
	SELECT v.name, '', 'indexed view' FROM sys.views v JOIN sys.schemas s ON s.schema_id=v.schema_id
	WHERE s.name=@p1 AND EXISTS (SELECT 1 FROM sys.indexes i WHERE i.object_id=v.object_id AND i.index_id>0)
	UNION ALL
	SELECT o.name, '', 'unsupported schema object '+o.type_desc COLLATE DATABASE_DEFAULT FROM sys.objects o JOIN sys.schemas s ON s.schema_id=o.schema_id
	WHERE s.name=@p1 AND o.is_ms_shipped=0 AND o.type NOT IN ('U','V','P','FN','IF','TF','TR','PK','UQ','F','C','D','SO')
	UNION ALL
	SELECT COALESCE(p.name, o.name), '', 'dependent default, check or column expression' FROM sys.objects o JOIN sys.schemas s ON s.schema_id=o.schema_id
	JOIN sys.sql_expression_dependencies d ON d.referencing_id=o.object_id
	LEFT JOIN sys.objects p ON p.object_id=o.parent_object_id
	WHERE s.name=@p1 AND o.type IN ('U','C','D') AND (p.object_id IS NULL OR p.type='U')
	AND (d.referenced_id IS NULL OR d.referenced_id<>COALESCE(NULLIF(o.parent_object_id,0),o.object_id))
) refused WHERE 1=1/*names*/ ORDER BY object_name, member_name, feature`

// readRefusals records what the native model cannot hold as refusals. It is the
// collecting form of refuseUnsupported.
func readRefusals(ctx context.Context, db Querier, c *catalog) error {
	return visit(ctx, db, schemaQuery{SQL: refusalQuery, Schema: c.schema, Names: c.names, NameColumn: "object_name"}, func(rows *sql.Rows) error {
		var object, member, feature string
		if err := rows.Scan(&object, &member, &feature); err != nil {
			return err
		}
		return c.collector.refuse(migrate.Name{Schema: c.schema, Name: object}, member, feature)
	})
}
