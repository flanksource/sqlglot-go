// Package sqlite reconciles SQLite tables with programmatic Atlas declarations.
// Filesystem-backed HCL migrations use the target-aware parent migrate package.
package sqlite

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	atlasmigrate "ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"
	atlas "ariga.io/atlas/sql/sqlite"
	"github.com/flanksource/commons/logger"
)

type ReconcileOptions struct {
	AllowDrops bool
	// AllowRebuilds retains source compatibility; supported rebuilds are automatic.
	AllowRebuilds bool
}

// ReconcileTables brings database's tables up to the tables declared: Atlas inspects
// each declared table, diffs it against its declaration, and applies the plan.
// Column and table drops are skipped unless AllowDrops is enabled. Other supported
// changes may rebuild tables. Views are outside the declared table schema.
//
// A column may be declared by its raw SQLite type alone (ColumnType.Raw); ReconcileTables
// parses it as inspection does, so a declaration and the table it created
// compare equal.
func ReconcileTables(ctx context.Context, database schema.ExecQuerier, options ReconcileOptions, tables ...*schema.Table) error {
	driver, err := atlas.Open(database)
	if err != nil {
		return fmt.Errorf("sqlite migrate: open driver: %w", err)
	}
	changes, err := diffTables(ctx, driver, options, tables)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil
	}
	names := tableNames(tables)
	plan, err := driver.PlanChanges(ctx, "", changes)
	if err != nil {
		return fmt.Errorf("sqlite migrate: plan %d changes: %w", len(changes), err)
	}
	if err := refuseUnmanagedTriggers(ctx, database, plan.Changes); err != nil {
		return err
	}
	log := logger.GetLogger("migrate")
	for _, change := range plan.Changes {
		log.Tracef("%s", change.Cmd)
	}
	if err := driver.ApplyChanges(ctx, changes); err != nil {
		return fmt.Errorf("sqlite migrate: apply %d changes: %w", len(changes), err)
	}
	log.V(1).Infof("Applied %d sqlite schema changes to %s", len(changes), strings.Join(names, ", "))
	return nil
}

// PendingChanges reports the changes ReconcileTables would apply, without applying them. It only
// reads the schema, so a caller can skip opening a migration transaction when nothing is pending.
// It applies the same drop policy as ReconcileTables.
func PendingChanges(ctx context.Context, database schema.ExecQuerier, options ReconcileOptions, tables ...*schema.Table) ([]schema.Change, error) {
	driver, err := atlas.Open(database)
	if err != nil {
		return nil, fmt.Errorf("sqlite migrate: open driver: %w", err)
	}
	return diffTables(ctx, driver, options, tables)
}

func diffTables(ctx context.Context, driver atlasmigrate.Driver, options ReconcileOptions, tables []*schema.Table) ([]schema.Change, error) {
	if len(tables) == 0 && !options.AllowDrops {
		return nil, errors.New("sqlite migrate: no table declared")
	}
	for _, table := range tables {
		if err := parseRawTypes(table); err != nil {
			return nil, err
		}
	}
	names := tableNames(tables)
	if options.AllowDrops {
		names = nil
	}
	current, err := driver.InspectSchema(ctx, "", &schema.InspectOptions{Mode: schema.InspectTables, Tables: names})
	if err != nil {
		return nil, fmt.Errorf("sqlite migrate: inspect %s: %w", strings.Join(names, ", "), err)
	}
	desired := desiredSchema(current, options, tables)
	changes, err := driver.SchemaDiff(current, desired)
	if err != nil {
		return nil, fmt.Errorf("sqlite migrate: diff %s: %w", strings.Join(names, ", "), err)
	}
	changes = withCheckExpressionChanges(current, desired, changes)
	return changes, nil
}

// desiredSchema retains omitted columns in the table definitions Atlas copies
// during rebuilds; filtering DropColumn changes alone would still lose them.
func desiredSchema(current *schema.Schema, options ReconcileOptions, tables []*schema.Table) *schema.Schema {
	desired := schema.New(current.Name)
	for _, table := range tables {
		cloned := *table
		cloned.Columns = slices.Clone(table.Columns)
		if previous, found := current.Table(table.Name); found && !options.AllowDrops {
			for _, column := range previous.Columns {
				if _, declared := table.Column(column.Name); declared {
					continue
				}
				preserved := *column
				preserved.Indexes, preserved.ForeignKeys = nil, nil
				cloned.AddColumns(&preserved)
				logger.GetLogger("migrate").Tracef("Skipping drop column of %s.%s", table.Name, column.Name)
			}
		}
		desired.AddTables(&cloned)
	}
	return desired
}

func tableNames(tables []*schema.Table) []string {
	names := make([]string, len(tables))
	for index, table := range tables {
		names[index] = table.Name
	}
	return names
}

// parseRawTypes gives every column declared by its raw type alone the type
// Atlas's own inspection parses that raw type into.
func parseRawTypes(table *schema.Table) error {
	for _, column := range table.Columns {
		if column.Type == nil {
			return fmt.Errorf("sqlite migrate: table %q column %q declares no type", table.Name, column.Name)
		}
		if column.Type.Type != nil {
			continue
		}
		parsed, err := atlas.ParseType(column.Type.Raw)
		if err != nil {
			return fmt.Errorf("sqlite migrate: table %q column %q type %q: %w", table.Name, column.Name, column.Type.Raw, err)
		}
		column.Type.Type = parsed
	}
	return nil
}

func refuseUnmanagedTriggers(ctx context.Context, database schema.ExecQuerier, changes []*atlasmigrate.Change) error {
	for _, change := range changes {
		modified, ok := change.Source.(*schema.ModifyTable)
		if !ok || !strings.HasPrefix(change.Cmd, "DROP TABLE ") {
			continue
		}
		rows, err := database.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type = 'trigger' AND tbl_name = ? LIMIT 1`, modified.T.Name)
		if err != nil {
			return fmt.Errorf("inspect triggers for table %q: %w", modified.T.Name, err)
		}
		var trigger string
		if rows.Next() {
			err = rows.Scan(&trigger)
		}
		if err == nil {
			err = rows.Err()
		}
		closeErr := rows.Close()
		if err != nil {
			return fmt.Errorf("inspect triggers for table %q: %w", modified.T.Name, err)
		}
		if closeErr != nil {
			return fmt.Errorf("close trigger inspection for table %q: %w", modified.T.Name, closeErr)
		}
		if trigger != "" {
			return fmt.Errorf("sqlite migrate: table %q has unmanaged trigger %q; refusing a rebuild that would discard it", modified.T.Name, trigger)
		}
	}
	return nil
}
