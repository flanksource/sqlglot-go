package sqlserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// maxRequestedColumns keeps one query under SQL Server's 2100 parameters: each
// requested column binds three names.
const maxRequestedColumns = 500

// maxPairsPerQuery bounds the object and index ids one detail query inlines.
const maxPairsPerQuery = 500

// requestedColumns is the CTE every dependents query starts from. The server
// resolves the requested schema, table and column names, so a name matches the
// way the database compares it, and each row keeps the position (ord) of the
// request it answers.
func requestedColumns(columns []engine.ColumnRef) (string, []any) {
	rows := make([]string, len(columns))
	args := make([]any, 0, 3*len(columns))
	for i, c := range columns {
		n := len(args)
		rows[i] = fmt.Sprintf("(%d, @p%d, @p%d, @p%d)", i, n+1, n+2, n+3)
		args = append(args, c.Table.Schema, c.Table.Name, c.Column)
	}
	return `WITH req AS (SELECT v.ord, c.object_id, c.column_id, c.name AS column_name
		FROM (VALUES ` + strings.Join(rows, ", ") + `) AS v(ord, schema_name, table_name, column_name)
		JOIN sys.schemas s ON s.name = v.schema_name
		JOIN sys.tables t ON t.schema_id = s.schema_id AND t.name = v.table_name
		JOIN sys.columns c ON c.object_id = t.object_id AND c.name = v.column_name) `, args
}

func collectRows[T any](ctx context.Context, q engine.Querier, text string, args []any, scan func(*sql.Rows) (T, error)) (out []T, err error) {
	rows, err := q.QueryContext(ctx, text, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		row, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

const resolvedColumnsQuery = `SELECT ord FROM req`

const indexHeadersQuery = `SELECT req.ord, s.name, t.name, i.object_id, i.index_id, i.name, i.type, i.type_desc,
	i.is_unique, i.is_primary_key, i.is_unique_constraint, i.is_disabled, i.is_hypothetical, i.fill_factor,
	i.is_padded, i.ignore_dup_key, i.allow_row_locks, i.allow_page_locks, COALESCE(i.filter_definition, ''),
	COALESCE(ds.type, ''), COALESCE(ds.name, ''), COALESCE(fg.is_default, 0),
	COALESCE((SELECT MIN(p.data_compression_desc) FROM sys.partitions p WHERE p.object_id = i.object_id AND p.index_id = i.index_id), ''),
	COALESCE((SELECT MAX(p.data_compression_desc) FROM sys.partitions p WHERE p.object_id = i.object_id AND p.index_id = i.index_id), ''),
	(SELECT COUNT(*) FROM sys.partitions p WHERE p.object_id = i.object_id AND p.index_id = i.index_id),
	COALESCE((SELECT st.no_recompute FROM sys.stats st WHERE st.object_id = i.object_id AND st.stats_id = i.index_id), 0),
	CASE WHEN EXISTS (SELECT 1 FROM sys.fulltext_indexes fi WHERE fi.object_id = i.object_id AND fi.unique_index_id = i.index_id) THEN 1 ELSE 0 END
FROM req
JOIN sys.columns col ON col.object_id = req.object_id AND col.column_id = req.column_id
JOIN sys.indexes i ON i.object_id = req.object_id AND i.name IS NOT NULL
JOIN sys.tables t ON t.object_id = i.object_id
JOIN sys.schemas s ON s.schema_id = t.schema_id
LEFT JOIN sys.data_spaces ds ON ds.data_space_id = i.data_space_id
LEFT JOIN sys.filegroups fg ON fg.data_space_id = ds.data_space_id
WHERE EXISTS (SELECT 1 FROM sys.index_columns ic WHERE ic.object_id = i.object_id AND ic.index_id = i.index_id AND ic.column_id = req.column_id)
	OR CHARINDEX(QUOTENAME(col.name), COALESCE(i.filter_definition, '')) > 0
ORDER BY req.ord, i.object_id, i.index_id`

const indexColumnsQuery = `SELECT ic.object_id, ic.index_id, c.name, ic.is_included_column, ic.is_descending_key
FROM sys.index_columns ic JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
WHERE EXISTS (SELECT 1 FROM (VALUES %s) m(o, i) WHERE m.o = ic.object_id AND m.i = ic.index_id)
ORDER BY ic.object_id, ic.index_id, ic.is_included_column, ic.key_ordinal, ic.index_column_id`

// foreignKeysQuery returns every column of each foreign key that has the column
// on its child or its parent side.
const foreignKeysQuery = `SELECT req.ord, fk.object_id, ps.name, pt.name, fk.name, pc.name, rs.name, rt.name, rc.name,
	fk.delete_referential_action_desc, fk.update_referential_action_desc, fk.is_not_trusted, fk.is_disabled, fk.is_not_for_replication
FROM req
JOIN sys.foreign_key_columns hit ON (hit.parent_object_id = req.object_id AND hit.parent_column_id = req.column_id)
	OR (hit.referenced_object_id = req.object_id AND hit.referenced_column_id = req.column_id)
JOIN sys.foreign_keys fk ON fk.object_id = hit.constraint_object_id
JOIN sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
JOIN sys.tables pt ON pt.object_id = fk.parent_object_id JOIN sys.schemas ps ON ps.schema_id = pt.schema_id
JOIN sys.columns pc ON pc.object_id = fkc.parent_object_id AND pc.column_id = fkc.parent_column_id
JOIN sys.tables rt ON rt.object_id = fk.referenced_object_id JOIN sys.schemas rs ON rs.schema_id = rt.schema_id
JOIN sys.columns rc ON rc.object_id = fkc.referenced_object_id AND rc.column_id = fkc.referenced_column_id
ORDER BY req.ord, fk.object_id, fkc.constraint_column_id`

const checksQuery = `SELECT req.ord, s.name, t.name, cc.name, cc.definition, cc.is_not_for_replication, cc.is_not_trusted, cc.is_disabled
FROM req
JOIN sys.columns col ON col.object_id = req.object_id AND col.column_id = req.column_id
JOIN sys.check_constraints cc ON cc.parent_object_id = req.object_id
	AND (cc.parent_column_id = req.column_id OR CHARINDEX(QUOTENAME(col.name), cc.definition) > 0)
JOIN sys.tables t ON t.object_id = cc.parent_object_id JOIN sys.schemas s ON s.schema_id = t.schema_id
ORDER BY req.ord, cc.name`

const statisticsQuery = `SELECT req.ord, s.name, t.name, st.object_id, st.stats_id, st.name, st.no_recompute, st.is_incremental, st.is_temporary,
	st.has_filter, COALESCE(st.filter_definition, '')
FROM req
JOIN sys.stats st ON st.object_id = req.object_id AND st.user_created = 1
JOIN sys.tables t ON t.object_id = st.object_id JOIN sys.schemas s ON s.schema_id = t.schema_id
WHERE EXISTS (SELECT 1 FROM sys.stats_columns sc WHERE sc.object_id = st.object_id AND sc.stats_id = st.stats_id AND sc.column_id = req.column_id)
ORDER BY req.ord, st.object_id, st.stats_id`

const statisticsColumnsQuery = `SELECT sc.object_id, sc.stats_id, c.name
FROM sys.stats_columns sc JOIN sys.columns c ON c.object_id = sc.object_id AND c.column_id = sc.column_id
WHERE EXISTS (SELECT 1 FROM (VALUES %s) m(o, i) WHERE m.o = sc.object_id AND m.i = sc.stats_id)
ORDER BY sc.object_id, sc.stats_id, sc.stats_column_id`

// schemaBoundQuery finds views and functions bound to the column, which SQL
// Server will not let the column change under.
const schemaBoundQuery = `SELECT req.ord, rs.name, ro.name, RTRIM(ro.type),
	CASE WHEN EXISTS (SELECT 1 FROM sys.indexes vi WHERE vi.object_id = ro.object_id AND vi.index_id > 0) THEN 1 ELSE 0 END
FROM req
JOIN sys.sql_expression_dependencies d ON d.referenced_class = 1 AND d.referenced_id = req.object_id
	AND d.referenced_minor_id = req.column_id AND d.is_schema_bound_reference = 1
JOIN sys.objects ro ON ro.object_id = d.referencing_id JOIN sys.schemas rs ON rs.schema_id = ro.schema_id
WHERE ro.type IN ('V', 'FN', 'IF', 'TF')
ORDER BY req.ord, rs.name, ro.name`

const computedColumnsQuery = `SELECT req.ord, s.name, t.name, cc.name
FROM req
JOIN sys.columns col ON col.object_id = req.object_id AND col.column_id = req.column_id
JOIN sys.computed_columns cc ON cc.object_id = req.object_id AND CHARINDEX(QUOTENAME(col.name), cc.definition) > 0
JOIN sys.tables t ON t.object_id = cc.object_id JOIN sys.schemas s ON s.schema_id = t.schema_id
ORDER BY req.ord, cc.name`

const fullTextColumnsQuery = `SELECT req.ord, s.name, t.name
FROM req
JOIN sys.fulltext_index_columns fic ON fic.object_id = req.object_id AND fic.column_id = req.column_id
JOIN sys.tables t ON t.object_id = req.object_id JOIN sys.schemas s ON s.schema_id = t.schema_id
ORDER BY req.ord`

// idPairs renders (object_id, id) pairs as a table value constructor; the
// values are integers read from the catalog, never text.
func idPairs(pairs [][2]int) string {
	rows := make([]string, len(pairs))
	for i, p := range pairs {
		rows[i] = fmt.Sprintf("(%d, %d)", p[0], p[1])
	}
	return strings.Join(rows, ", ")
}
