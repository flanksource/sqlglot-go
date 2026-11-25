package sqlserver

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// Kinds of dependent the planner cannot re-create. Each is only ever refused.
const (
	dependentFunction       engine.DependentKind = "function"
	dependentComputedColumn engine.DependentKind = "computed-column"
	dependentFullText       engine.DependentKind = "fulltext-index"
)

// boundDependents lists what pins a column without a definition the planner can
// re-create: schema-bound views and functions (indexed views among them),
// computed columns and full-text indexes. Each carries a Refusal.
func (r *reader) boundDependents(ctx context.Context) ([]engine.Dependent, error) {
	var out []engine.Dependent
	bound, err := collectRows(ctx, r.q, r.cte+schemaBoundQuery, r.args, func(rows *sql.Rows) (engine.Dependent, error) {
		var ord, indexed int
		var kind string
		var name migrate.Name
		if err := rows.Scan(&ord, &name.Schema, &name.Name, &kind, &indexed); err != nil {
			return engine.Dependent{}, err
		}
		return schemaBoundDependent(r.requested[ord], name, kind, indexed == 1), nil
	})
	if err != nil {
		return nil, fmt.Errorf("read schema-bound objects: %w", err)
	}
	out = append(out, bound...)
	computed, err := collectRows(ctx, r.q, r.cte+computedColumnsQuery, r.args, func(rows *sql.Rows) (engine.Dependent, error) {
		var ord int
		var table migrate.Name
		var column string
		if err := rows.Scan(&ord, &table.Schema, &table.Name, &column); err != nil {
			return engine.Dependent{}, err
		}
		return engine.Dependent{Kind: dependentComputedColumn, Table: table, Name: column, On: []engine.ColumnRef{r.requested[ord]},
			Refusal: fmt.Sprintf("computed column %s on %s uses %s.%s, and the planner cannot re-create computed columns", column, qualified(table), qualified(r.requested[ord].Table), r.requested[ord].Column)}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("read computed columns: %w", err)
	}
	out = append(out, computed...)
	fullText, err := collectRows(ctx, r.q, r.cte+fullTextColumnsQuery, r.args, func(rows *sql.Rows) (engine.Dependent, error) {
		var ord int
		var table migrate.Name
		if err := rows.Scan(&ord, &table.Schema, &table.Name); err != nil {
			return engine.Dependent{}, err
		}
		return engine.Dependent{Kind: dependentFullText, Table: table, Name: "full-text index", On: []engine.ColumnRef{r.requested[ord]},
			Refusal: fmt.Sprintf("column %s.%s is in the full-text index of %s, which the planner cannot re-create", qualified(r.requested[ord].Table), r.requested[ord].Column, qualified(table))}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("read full-text columns: %w", err)
	}
	return append(out, fullText...), nil
}

// schemaBoundDependent describes a view or function bound to the column.
func schemaBoundDependent(on engine.ColumnRef, name migrate.Name, objectType string, indexed bool) engine.Dependent {
	kind, label := dependentFunction, "schema-bound function"
	if objectType == "V" {
		kind, label = engine.DependentView, "schema-bound view"
		if indexed {
			label = "indexed view"
		}
	}
	return engine.Dependent{Kind: kind, Table: name, Name: name.Name, On: []engine.ColumnRef{on},
		Refusal: fmt.Sprintf("%s %s is bound to %s.%s, and the planner cannot re-create it", label, qualified(name), qualified(on.Table), on.Column)}
}
