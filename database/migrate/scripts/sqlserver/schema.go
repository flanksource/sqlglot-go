package sqlserver

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	catalog "github.com/flanksource/sqlglot-go/database/inspect/catalog/sqlserver"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	"github.com/flanksource/sqlglot-go/sqlglot"
)

type schemaSnapshot struct {
	Columns      []catalog.Column
	Indexes      []catalog.Index
	IndexColumns []catalog.IndexColumn
	ForeignKeys  []catalog.ForeignKey
	Checks       []catalog.CheckConstraint
	Details      []catalog.ColumnDetail
}

func (s schemaSnapshot) column(name string) (schemaSnapshot, error) {
	selected := schemaSnapshot{}
	for _, column := range s.Columns {
		if column.Name == name {
			column.Position = 0
			selected.Columns = append(selected.Columns, column)
		}
	}
	if len(selected.Columns) != 1 {
		return selected, fmt.Errorf("schema column %s is missing or not visible", name)
	}
	indexes := map[string]bool{}
	for _, column := range s.IndexColumns {
		if column.Column == name {
			indexes[column.Index] = true
		}
	}
	for _, index := range s.Indexes {
		if slices.Contains(index.KeyColumns, name) {
			indexes[index.Name] = true
		}
		if indexes[index.Name] {
			selected.Indexes = append(selected.Indexes, index)
		}
	}
	for _, column := range s.IndexColumns {
		if indexes[column.Index] {
			selected.IndexColumns = append(selected.IndexColumns, column)
		}
	}
	foreignKeys := map[string]bool{}
	for _, fk := range s.ForeignKeys {
		if fk.Column == name {
			foreignKeys[fk.Name] = true
		}
	}
	for _, fk := range s.ForeignKeys {
		if foreignKeys[fk.Name] {
			selected.ForeignKeys = append(selected.ForeignKeys, fk)
		}
	}
	for _, detail := range s.Details {
		if detail.Name == name {
			selected.Details = append(selected.Details, detail)
		}
	}
	for _, check := range s.Checks {
		if check.Column != nil {
			if *check.Column == name {
				selected.Checks = append(selected.Checks, check)
			}
			continue
		}
		parsed, err := sqlglot.ParseOne("SELECT 1 WHERE "+check.Definition, "tsql")
		if err != nil {
			return selected, fmt.Errorf("resolve check %s column dependencies: %w", check.Name, err)
		}
		matched := false
		parsed.Walk(func(e *sqlglot.Expression) bool {
			if e.Class == "Column" && e.Name() == name {
				matched = true
			}
			return true
		})
		if matched {
			selected.Checks = append(selected.Checks, check)
		}
	}
	return selected, nil
}

func (o *observer) schema(ctx context.Context, name string) error {
	parts := strings.Split(name, ".")
	if len(parts) != 2 && len(parts) != 3 {
		return fmt.Errorf("schema dependency requires schema.table[.column]: %s", name)
	}
	q := catalog.FromDB(o.q)
	if len(parts) == 3 {
		column, err := catalog.ResolveColumn(ctx, q, catalog.ColumnName{Schema: parts[0], Table: parts[1], Column: parts[2]})
		if err != nil {
			return err
		}
		parts = []string{column.Schema, column.Table, column.Column}
		name = strings.Join(parts, ".")
	}
	scope := catalog.Scope{Schema: parts[0], Names: []string{parts[1]}}
	var snapshot schemaSnapshot
	var err error
	snapshot.Columns, err = catalog.Columns(ctx, q, scope)
	if err != nil {
		return err
	}
	if len(snapshot.Columns) == 0 {
		return fmt.Errorf("schema dependency %s is missing or not visible", name)
	}
	if snapshot.Indexes, err = catalog.Indexes(ctx, q, scope); err != nil {
		return err
	}
	if snapshot.IndexColumns, err = catalog.IndexColumns(ctx, q, scope); err != nil {
		return err
	}
	if snapshot.ForeignKeys, err = catalog.ForeignKeys(ctx, q, scope); err != nil {
		return err
	}
	if snapshot.Checks, err = catalog.CheckConstraints(ctx, q, scope); err != nil {
		return err
	}
	if snapshot.Details, err = catalog.ColumnDetails(ctx, q, scope); err != nil {
		return err
	}
	if len(parts) == 3 {
		if snapshot, err = snapshot.column(parts[2]); err != nil {
			return err
		}
	}
	value, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	o.inputs["schema:"+name] = scripts.Observation{Kind: "schema-dependency", Hash: scripts.SourceHash(string(value))}
	return nil
}
