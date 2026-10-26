// Package snapshot inspects complete SQL Server schemas for migration planning.
// Query bounds used by the interactive explorer never apply to these snapshots.
package snapshot

import (
	"context"
	"fmt"
	"slices"

	"github.com/flanksource/clicky/task"
	"github.com/flanksource/sqlglot-go/database/inspect/catalog/sqlserver"
	"github.com/flanksource/sqlglot-go/migrate"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
)

type Options struct {
	Schemas []string
	// Task, when set, reports each catalog read as progress.
	Task *task.Task
}
type Result struct {
	Schema  migrate.Schema
	Schemas []string
}

type catalog struct {
	schema              string
	tables              []mssqlcatalog.Table
	columns             []mssqlcatalog.Column
	indexes             []mssqlcatalog.Index
	parts               []mssqlcatalog.IndexColumn
	keys                []mssqlcatalog.ForeignKey
	checks              []mssqlcatalog.CheckConstraint
	routines            []mssqlcatalog.Routine
	modules             []migrate.Definition
	collations          map[[2]string]string
	fillFactors         map[[2]string]int
	rowGUIDs            map[[2]string]bool
	systemNamedDefaults map[[2]string]bool
	quotedIdentifierOff map[string]bool
	sequences           []migrate.Sequence
	tableTypes          []migrate.TableType
	dependencies        map[string][]migrate.Name
	runtimeDependencies map[string][]migrate.RuntimeDependency
}

func Inspect(ctx context.Context, db Querier, options Options) (Result, error) {
	if ctx == nil || db == nil || len(options.Schemas) == 0 {
		return Result{}, fmt.Errorf("migration snapshot requires a context, database and explicit schema scope")
	}
	var permission int
	if err := db.QueryRowContext(ctx, "SELECT COALESCE(HAS_PERMS_BY_NAME(DB_NAME(), 'DATABASE', 'VIEW DEFINITION'), 0)").Scan(&permission); err != nil {
		return Result{}, fmt.Errorf("inspect migration permissions: %w", err)
	}
	if permission != 1 {
		return Result{}, fmt.Errorf("complete migration inspection requires database VIEW DEFINITION permission")
	}
	result := Result{Schemas: slices.Clone(options.Schemas)}
	seen := map[string]bool{}
	dependencies := map[migrate.Name][]migrate.Name{}
	runtimeDependencies := map[migrate.Name][]migrate.RuntimeDependency{}
	for i, name := range options.Schemas {
		if seen[name] {
			return Result{}, fmt.Errorf("duplicate migration schema %q", name)
		}
		seen[name] = true
		c, err := read(ctx, db, name, func(step string, done, total int) {
			if options.Task != nil {
				options.Task.SetDescription(fmt.Sprintf("reading %s %s", name, step))
				options.Task.SetProgress(i*total+done, len(options.Schemas)*total)
			}
		})
		if err != nil {
			return Result{}, fmt.Errorf("inspect schema %q: %w", name, err)
		}
		schema, err := build(c)
		if err != nil {
			return Result{}, fmt.Errorf("inspect schema %q: %w", name, err)
		}
		result.Schema.Tables = append(result.Schema.Tables, schema.Tables...)
		result.Schema.Definitions = append(result.Schema.Definitions, schema.Definitions...)
		result.Schema.Sequences = append(result.Schema.Sequences, schema.Sequences...)
		result.Schema.TableTypes = append(result.Schema.TableTypes, schema.TableTypes...)
		for owner, refs := range c.dependencies {
			dependencies[migrate.Name{Schema: name, Name: owner}] = refs
		}
		for owner, refs := range c.runtimeDependencies {
			runtimeDependencies[migrate.Name{Schema: name, Name: owner}] = refs
		}
	}
	for i := range result.Schema.Definitions {
		d := &result.Schema.Definitions[i]
		if !seen[d.Name.Schema] {
			return Result{}, fmt.Errorf("definition %v is outside the inspected schema scope", d.Name)
		}
		d.Dependencies = dependencies[d.Name]
		d.RuntimeDependencies = runtimeDependencies[d.Name]
	}
	if options.Task != nil {
		options.Task.SetDescription("validating snapshot")
	}
	if _, err := planner.Plan(migrate.Request{Current: result.Schema, Desired: result.Schema}); err != nil {
		return Result{}, fmt.Errorf("migration snapshot validation: %w", err)
	}
	return result, nil
}

func read(ctx context.Context, db Querier, name string, report func(step string, done, total int)) (catalog, error) {
	c := catalog{schema: name, collations: map[[2]string]string{}, fillFactors: map[[2]string]int{}, rowGUIDs: map[[2]string]bool{}, systemNamedDefaults: map[[2]string]bool{},
		quotedIdentifierOff: map[string]bool{}, dependencies: map[string][]migrate.Name{}, runtimeDependencies: map[string][]migrate.RuntimeDependency{}}
	q, scope := mssqlcatalog.FromDB(db), mssqlcatalog.Scope{Schema: name}
	found, err := mssqlcatalog.Schemas(ctx, q, []string{name})
	if err != nil {
		return c, err
	}
	if len(found) != 1 {
		return c, fmt.Errorf("schema %q was not found", name)
	}
	steps := []struct {
		name string
		run  func() error
	}{
		{"tables", func() error { var err error; c.tables, err = mssqlcatalog.Tables(ctx, q, scope, ""); return err }},
		{"columns", func() error { var err error; c.columns, err = mssqlcatalog.Columns(ctx, q, scope); return err }},
		{"indexes", func() error { var err error; c.indexes, err = mssqlcatalog.Indexes(ctx, q, scope); return err }},
		{"index columns", func() error { var err error; c.parts, err = mssqlcatalog.IndexColumns(ctx, q, scope); return err }},
		{"foreign keys", func() error { var err error; c.keys, err = mssqlcatalog.ForeignKeys(ctx, q, scope); return err }},
		{"check constraints", func() error { var err error; c.checks, err = mssqlcatalog.CheckConstraints(ctx, q, scope); return err }},
		{"routines", func() error { var err error; c.routines, err = mssqlcatalog.Routines(ctx, q, scope); return err }},
		{"collations", func() error { return readCollations(ctx, db, &c) }},
		{"index options", func() error { return readIndexOptions(ctx, db, &c) }},
		{"rowguid columns", func() error { return readRowGUIDs(ctx, db, &c) }},
		{"system-named defaults", func() error { return readSystemNamedDefaults(ctx, db, &c) }},
		{"module settings", func() error { return readModuleSettings(ctx, db, &c) }},
		{"sequences", func() error { return readSequences(ctx, db, &c) }},
		{"table types", func() error { return readTableTypes(ctx, db, &c) }},
		{"triggers", func() error { return readTriggers(ctx, db, &c) }},
		{"dependencies", func() error { return readDependencies(ctx, db, &c) }},
		{"unsupported objects", func() error { return refuseUnsupported(ctx, db, name) }},
	}
	for i, step := range steps {
		report(step.name, i, len(steps))
		if err := step.run(); err != nil {
			return c, fmt.Errorf("read %s: %w", step.name, err)
		}
	}
	report("done", len(steps), len(steps))
	return c, nil
}
