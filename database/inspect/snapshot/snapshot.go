// Package snapshot inspects SQL Server schemas for migration planning, whole or
// narrowed to named objects. Query bounds used by the interactive explorer
// never apply to these snapshots.
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

// maxNamesPerRead bounds the name filter of one catalog query, which binds each
// name as a parameter.
const maxNamesPerRead = 800

type Options struct {
	Schemas []string
	// Task, when set, reports each catalog read as progress.
	Task *task.Task
	// Objects narrows the inspection to these tables, views, routines,
	// triggers, sequences and table types, read with name filters; each must be
	// in a schema of Schemas. An object that does not exist is not in the
	// result. Empty reads every object of Schemas.
	Objects []migrate.Name
	// CollectRefusals reports what the native model cannot hold in
	// Result.Refusals instead of failing the inspection: a whole-object refusal
	// leaves the object out of Schema, a member refusal leaves it in.
	CollectRefusals bool
}
type Result struct {
	Schema  migrate.Schema
	Schemas []string
	// Warnings names each definition whose body is not understood. The snapshot
	// still holds it, as written.
	Warnings []string
	// Refusals is set only with Options.CollectRefusals.
	Refusals []Refusal
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
	// names narrows every read to these objects of the schema; empty reads all.
	names []string
	// collector is nil for a strict inspection.
	collector *collector
	// absent is set for a narrowed read of a schema that does not exist.
	absent bool
}

// namesIn returns the object names of Options.Objects in schema, in chunks the
// catalog can bind. A whole-schema inspection is the one chunk of no names.
func (o Options) namesIn(schema string) [][]string {
	if len(o.Objects) == 0 {
		return [][]string{nil}
	}
	var names []string
	for _, n := range o.Objects {
		if n.Schema == schema && !slices.Contains(names, n.Name) {
			names = append(names, n.Name)
		}
	}
	return slices.Collect(slices.Chunk(names, maxNamesPerRead))
}

func (o Options) validate(ctx context.Context, db Querier) error {
	if ctx == nil || db == nil || len(o.Schemas) == 0 {
		return fmt.Errorf("migration snapshot requires a context, database and explicit schema scope")
	}
	for _, n := range o.Objects {
		if !slices.Contains(o.Schemas, n.Schema) {
			return fmt.Errorf("object %s.%s is outside the inspected schema scope", n.Schema, n.Name)
		}
	}
	return nil
}

// moduleRefs holds each module's dependencies by owner, which only a complete
// read of its schema can attach to the definitions.
type moduleRefs struct {
	local   map[migrate.Name][]migrate.Name
	runtime map[migrate.Name][]migrate.RuntimeDependency
}

func Inspect(ctx context.Context, db Querier, options Options) (Result, error) {
	if err := options.validate(ctx, db); err != nil {
		return Result{}, err
	}
	if err := requireViewDefinition(ctx, db); err != nil {
		return Result{}, err
	}
	result := Result{Schemas: slices.Clone(options.Schemas)}
	refs := moduleRefs{local: map[migrate.Name][]migrate.Name{}, runtime: map[migrate.Name][]migrate.RuntimeDependency{}}
	seen := map[string]bool{}
	for i, name := range options.Schemas {
		if seen[name] {
			return Result{}, fmt.Errorf("duplicate migration schema %q", name)
		}
		seen[name] = true
		report := func(step string, done, total int) {
			if options.Task != nil {
				options.Task.SetDescription(fmt.Sprintf("reading %s %s", name, step))
				options.Task.SetProgress(i*total+done, len(options.Schemas)*total)
			}
		}
		for _, names := range options.namesIn(name) {
			if err := inspectChunk(ctx, db, options, readRequest{schema: name, names: names, report: report}, &result, refs); err != nil {
				return Result{}, fmt.Errorf("inspect schema %q: %w", name, err)
			}
		}
	}
	for i := range result.Schema.Definitions {
		d := &result.Schema.Definitions[i]
		if !seen[d.Name.Schema] {
			return Result{}, fmt.Errorf("definition %v is outside the inspected schema scope", d.Name)
		}
		d.Dependencies = refs.local[d.Name]
		d.RuntimeDependencies = refs.runtime[d.Name]
	}
	if options.Task != nil {
		options.Task.SetDescription("validating snapshot")
	}
	warnings, err := validateSnapshot(result.Schema)
	if err != nil {
		return Result{}, err
	}
	result.Warnings = warnings
	return result, nil
}

// validateSnapshot checks that the planner can read what was inspected by
// diffing the schema against itself. A narrowed or refusing inspection may
// hold a foreign key whose target it did not read, which the diff accepts.
func validateSnapshot(s migrate.Schema) ([]string, error) {
	diff, err := planner.Planner{}.Diff(s, s)
	if err != nil {
		return nil, fmt.Errorf("migration snapshot validation: %w", err)
	}
	return diff.Warnings(), nil
}

func inspectChunk(ctx context.Context, db Querier, options Options, req readRequest, result *Result, refs moduleRefs) error {
	if options.CollectRefusals {
		req.collector = newCollector()
	}
	c, err := readNamed(ctx, db, req)
	if err != nil {
		return err
	}
	if c.absent {
		return nil
	}
	schema, err := build(c)
	if err != nil {
		return err
	}
	schema = c.collector.dropOmitted(schema)
	result.Schema.Tables = append(result.Schema.Tables, schema.Tables...)
	result.Schema.Definitions = append(result.Schema.Definitions, schema.Definitions...)
	result.Schema.Sequences = append(result.Schema.Sequences, schema.Sequences...)
	result.Schema.TableTypes = append(result.Schema.TableTypes, schema.TableTypes...)
	result.Refusals = append(result.Refusals, c.collector.merged()...)
	for owner, deps := range c.dependencies {
		refs.local[migrate.Name{Schema: req.schema, Name: owner}] = deps
	}
	for owner, deps := range c.runtimeDependencies {
		refs.runtime[migrate.Name{Schema: req.schema, Name: owner}] = deps
	}
	return nil
}

// readRequest is one catalog read: a schema, the names to narrow it to (none
// for the whole schema), and the collector that receives what cannot be modelled.
type readRequest struct {
	schema    string
	names     []string
	collector *collector
	report    func(step string, done, total int)
}

func read(ctx context.Context, db Querier, name string, report func(step string, done, total int)) (catalog, error) {
	return readNamed(ctx, db, readRequest{schema: name, report: report})
}

func readNamed(ctx context.Context, db Querier, req readRequest) (catalog, error) {
	name := req.schema
	c := catalog{schema: name, names: req.names, collector: req.collector, collations: map[[2]string]string{}, fillFactors: map[[2]string]int{}, rowGUIDs: map[[2]string]bool{}, systemNamedDefaults: map[[2]string]bool{},
		quotedIdentifierOff: map[string]bool{}, dependencies: map[string][]migrate.Name{}, runtimeDependencies: map[string][]migrate.RuntimeDependency{}}
	q, scope := mssqlcatalog.FromDB(db), mssqlcatalog.Scope{Schema: name, Names: req.names}
	found, err := mssqlcatalog.Schemas(ctx, q, []string{name})
	if err != nil {
		return c, err
	}
	if len(found) == 0 && len(req.names) > 0 {
		c.absent = true
		return c, nil
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
		{"unsupported objects", func() error { return c.refuseUnsupported(ctx, db) }},
	}
	for i, step := range steps {
		if req.report != nil {
			req.report(step.name, i, len(steps))
		}
		if err := step.run(); err != nil {
			return c, fmt.Errorf("read %s: %w", step.name, err)
		}
	}
	if req.report != nil {
		req.report("done", len(steps), len(steps))
	}
	return c, nil
}

// refuseUnsupported fails on the first unsupported object, or records each as a refusal.
func (c *catalog) refuseUnsupported(ctx context.Context, db Querier) error {
	if c.collector == nil {
		return refuseUnsupported(ctx, db, c.schema)
	}
	return readRefusals(ctx, db, c)
}
