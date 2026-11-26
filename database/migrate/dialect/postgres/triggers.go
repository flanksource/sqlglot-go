package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/sqlglot-go/database/migrate/dialect/atlasdriver"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// dependentTriggers returns the user triggers whose UPDATE OF list or WHEN
// clause reads one of the given columns: PostgreSQL records exactly those as
// column dependencies of the trigger, and refuses to retype or drop such a
// column while the trigger exists. A column the trigger function reads is not
// recorded and so not found.
const dependentTriggers = `
WITH targets(idx, rel, col) AS (VALUES %s)
SELECT n.nspname, c.relname, t.tgname, pg_get_triggerdef(t.oid, true), targets.idx
FROM targets
JOIN pg_class c ON c.oid = to_regclass(targets.rel)
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_attribute a ON a.attrelid = c.oid AND a.attname = targets.col AND NOT a.attisdropped
JOIN pg_depend d ON d.classid = 'pg_trigger'::regclass AND d.refclassid = 'pg_class'::regclass
 AND d.refobjid = c.oid AND d.refobjsubid = a.attnum
JOIN pg_trigger t ON t.oid = d.objid AND NOT t.tgisinternal
ORDER BY 1, 2, 3, 5`

// triggerDependents lists the triggers that read the forcing columns, each
// carrying a Refusal: the engine cannot re-create a trigger, and it belongs to
// whatever created it (a bundle's post scripts). A table drop forces none,
// since the triggers go with the table.
func triggerDependents(ctx context.Context, q engine.Querier, columns []engine.ColumnRef) (_ []engine.Dependent, returnErr error) {
	var values []string
	var args []any
	for i, c := range columns {
		if c.Column == "" || c.Dropped == engine.DroppedTable {
			continue
		}
		n := len(args)
		values = append(values, fmt.Sprintf("($%d::int, $%d::text, $%d::text)", n+1, n+2, n+3))
		args = append(args, i, atlasdriver.QuoteIdent(c.Table.Schema)+"."+atlasdriver.QuoteIdent(c.Table.Name), c.Column)
	}
	if len(values) == 0 {
		return nil, nil
	}
	rows, err := q.QueryContext(ctx, fmt.Sprintf(dependentTriggers, strings.Join(values, ", ")), args...)
	if err != nil {
		return nil, fmt.Errorf("inspect trigger dependencies: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	var out []engine.Dependent
	for rows.Next() {
		var schemaName, table, name, definition string
		var idx int
		if err := rows.Scan(&schemaName, &table, &name, &definition, &idx); err != nil {
			return nil, err
		}
		col := columns[idx]
		if last := len(out) - 1; last >= 0 && out[last].Table.Name == table && out[last].Table.Schema == schemaName && out[last].Name == name {
			out[last].On = append(out[last].On, col)
			continue
		}
		out = append(out, engine.Dependent{
			Kind: engine.DependentTrigger, Table: migrate.Name{Schema: schemaName, Name: table}, Name: name,
			On: []engine.ColumnRef{col}, Definition: definition,
			Refusal: fmt.Sprintf("trigger %s on %s.%s reads column %s, and PostgreSQL refuses to retype or drop a column a trigger reads; drop the trigger first and re-create it afterwards: %s",
				name, schemaName, table, col.Column, definition),
		})
	}
	return out, rows.Err()
}
