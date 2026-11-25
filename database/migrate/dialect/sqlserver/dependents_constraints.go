package sqlserver

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

type foreignKeyRow struct {
	ord                         int
	objectID                    int
	table                       migrate.Name
	name, column                string
	references                  migrate.Name
	referencedColumn            string
	onDelete, onUpdate          string
	untrusted, disabled, noRepl bool
}

func scanForeignKeyRow(rows *sql.Rows) (foreignKeyRow, error) {
	var f foreignKeyRow
	err := rows.Scan(&f.ord, &f.objectID, &f.table.Schema, &f.table.Name, &f.name, &f.column, &f.references.Schema, &f.references.Name,
		&f.referencedColumn, &f.onDelete, &f.onUpdate, &f.untrusted, &f.disabled, &f.noRepl)
	return f, err
}

// foreignKeyDependents reads the keys with the column on either side. Their
// rows arrive grouped by key in column order, which pairs each child column
// with the column it references.
func (r *reader) foreignKeyDependents(ctx context.Context) ([]engine.Dependent, error) {
	rows, err := collectRows(ctx, r.q, r.cte+foreignKeysQuery, r.args, scanForeignKeyRow)
	if err != nil {
		return nil, fmt.Errorf("read foreign keys: %w", err)
	}
	var out []engine.Dependent
	byKey := map[int]int{}
	paired := map[[2]string]bool{}
	for _, row := range rows {
		i, ok := byKey[row.objectID]
		if !ok {
			byKey[row.objectID] = len(out)
			i = len(out)
			out = append(out, foreignKeyDependent(row))
		}
		// Every requested column that hits the key repeats the key's rows.
		if pair := [2]string{fmt.Sprint(row.objectID, row.column), row.referencedColumn}; !paired[pair] {
			paired[pair] = true
			out[i].ForeignKey.Columns = append(out[i].ForeignKey.Columns, row.column)
			out[i].ForeignKey.ReferencedColumns = append(out[i].ForeignKey.ReferencedColumns, row.referencedColumn)
		}
		out[i].On = append(out[i].On, r.requested[row.ord])
	}
	return out, nil
}

func foreignKeyDependent(row foreignKeyRow) engine.Dependent {
	var reasons []string
	if row.disabled {
		reasons = append(reasons, "is disabled")
	}
	if row.untrusted {
		reasons = append(reasons, "is not trusted")
	}
	return engine.Dependent{Kind: engine.DependentForeignKey, Table: row.table, Name: row.name,
		ForeignKey: &migrate.ForeignKey{Name: row.name, References: row.references, OnDelete: normalizeAction(row.onDelete),
			OnUpdate: normalizeAction(row.onUpdate), NotForReplication: row.noRepl},
		Refusal: refusalText("foreign key "+row.name+" on "+qualified(row.table), reasons)}
}

type checkRow struct {
	ord                         int
	table                       migrate.Name
	name, definition            string
	noRepl, untrusted, disabled bool
}

func scanCheckRow(rows *sql.Rows) (checkRow, error) {
	var c checkRow
	err := rows.Scan(&c.ord, &c.table.Schema, &c.table.Name, &c.name, &c.definition, &c.noRepl, &c.untrusted, &c.disabled)
	return c, err
}

func (r *reader) checkDependents(ctx context.Context) ([]engine.Dependent, error) {
	rows, err := collectRows(ctx, r.q, r.cte+checksQuery, r.args, scanCheckRow)
	if err != nil {
		return nil, fmt.Errorf("read check constraints: %w", err)
	}
	out := make([]engine.Dependent, len(rows))
	for i, c := range rows {
		var reasons []string
		if c.disabled {
			reasons = append(reasons, "is disabled")
		}
		if c.untrusted {
			reasons = append(reasons, "is not trusted")
		}
		out[i] = engine.Dependent{Kind: engine.DependentCheck, Table: c.table, Name: c.name, On: []engine.ColumnRef{r.requested[c.ord]},
			Check:   &migrate.Check{Name: c.name, Expression: normalizeExpression(c.definition), NotForReplication: c.noRepl},
			Refusal: refusalText("check constraint "+c.name+" on "+qualified(c.table), reasons)}
	}
	return out, nil
}

type statisticRow struct {
	ord                      int
	table                    migrate.Name
	objectID, statsID        int
	name                     string
	noRecompute, incremental bool
	temporary, hasFilter     bool
	filter                   string
}

func scanStatisticRow(rows *sql.Rows) (statisticRow, error) {
	var s statisticRow
	err := rows.Scan(&s.ord, &s.table.Schema, &s.table.Name, &s.objectID, &s.statsID, &s.name, &s.noRecompute, &s.incremental, &s.temporary, &s.hasFilter, &s.filter)
	return s, err
}

func (r *reader) statisticDependents(ctx context.Context) ([]engine.Dependent, error) {
	rows, err := collectRows(ctx, r.q, r.cte+statisticsQuery, r.args, scanStatisticRow)
	if err != nil || len(rows) == 0 {
		return nil, wrapRead("statistics", err)
	}
	columns, err := r.statisticColumns(ctx, rows)
	if err != nil {
		return nil, err
	}
	out := make([]engine.Dependent, len(rows))
	for i, s := range rows {
		var reasons []string
		if s.incremental {
			reasons = append(reasons, "is incremental")
		}
		if s.temporary {
			reasons = append(reasons, "is temporary")
		}
		out[i] = engine.Dependent{Kind: engine.DependentStatistic, Table: s.table, Name: s.name, On: []engine.ColumnRef{r.requested[s.ord]},
			Definition: statisticDefinition(s, columns[indexKey{s.objectID, s.statsID}]),
			Refusal:    refusalText("statistic "+s.name+" on "+qualified(s.table), reasons)}
	}
	return out, nil
}

func wrapRead(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("read %s: %w", what, err)
}

func (r *reader) statisticColumns(ctx context.Context, rows []statisticRow) (map[indexKey][]string, error) {
	var pairs [][2]int
	seen := map[indexKey]bool{}
	for _, s := range rows {
		if k := (indexKey{s.objectID, s.statsID}); !seen[k] {
			seen[k] = true
			pairs = append(pairs, [2]int{s.objectID, s.statsID})
		}
	}
	columns := map[indexKey][]string{}
	for len(pairs) > 0 {
		n := min(len(pairs), maxPairsPerQuery)
		found, err := collectRows(ctx, r.q, fmt.Sprintf(statisticsColumnsQuery, idPairs(pairs[:n])), nil, func(rows *sql.Rows) (struct {
			key    indexKey
			column string
		}, error) {
			var row struct {
				key    indexKey
				column string
			}
			err := rows.Scan(&row.key.objectID, &row.key.indexID, &row.column)
			return row, err
		})
		if err != nil {
			return nil, fmt.Errorf("read statistics columns: %w", err)
		}
		for _, f := range found {
			columns[f.key] = append(columns[f.key], f.column)
		}
		pairs = pairs[n:]
	}
	return columns, nil
}

// statisticDefinition is the complete CREATE STATISTICS statement for a user
// statistic: its columns in order, its filter and NORECOMPUTE. The sample rate
// is not part of what the catalog keeps, so a re-created statistic is sampled
// by the server's default.
func statisticDefinition(s statisticRow, columns []string) string {
	text := "CREATE STATISTICS " + quote(s.name) + " ON " + qualified(s.table) + " (" + quoteAll(columns) + ")"
	if s.hasFilter {
		text += " WHERE " + normalizeExpression(s.filter)
	}
	if s.noRecompute {
		text += " WITH NORECOMPUTE"
	}
	return text
}
