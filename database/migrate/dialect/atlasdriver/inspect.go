package atlasdriver

import (
	"context"
	"errors"
	"fmt"
	"slices"

	atlas "ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/changeset"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

const tableMode = schema.InspectSchemas | schema.InspectTables | schema.InspectTypes

// scoped answers whether an object is managed by the scope being inspected.
type scoped struct {
	complete bool
	objects  map[migrate.Name]bool
}

func (s scoped) has(n migrate.Name) bool { return s.complete || s.objects[n] }

// Inspect reads one schema. Declared scope returns exactly the scope's objects
// that exist; complete scope returns the whole schema. Objects that cannot be
// managed faithfully come back as per-object refusals, never as a failure of
// the schema, so an untouched one never blocks a plan.
func (d *Dialect) Inspect(ctx context.Context, q engine.Querier, scope engine.Scope) (engine.Inspection, error) {
	name, err := d.schemaName(scope)
	if err != nil {
		return engine.Inspection{}, err
	}
	sc := scoped{complete: scope.Mode == engine.ScopeComplete, objects: map[migrate.Name]bool{}}
	for _, o := range scope.Objects {
		if o.Schema != name {
			return engine.Inspection{}, fmt.Errorf("%s: scope object %s.%s is outside schema %q", d.Name(), o.Schema, o.Name, name)
		}
		sc.objects[o] = true
	}
	drv, err := d.atlas.Open(q)
	if err != nil {
		return engine.Inspection{}, fmt.Errorf("%s: open inspector: %w", d.Name(), err)
	}
	s, err := d.readSchema(ctx, drv, name, scope)
	if err != nil {
		return engine.Inspection{}, err
	}
	extras, err := d.backend.Extras(ctx, q, name)
	if err != nil {
		return engine.Inspection{}, fmt.Errorf("%s: inspect unsupported objects: %w", d.Name(), err)
	}
	return d.assemble(s, extras, sc)
}

func (d *Dialect) schemaName(scope engine.Scope) (string, error) {
	if len(scope.Schemas) != 1 {
		return "", fmt.Errorf("%s: scope must name exactly one schema, got %d: %v", d.Name(), len(scope.Schemas), scope.Schemas)
	}
	if err := d.backend.ValidateSchema(scope.Schemas[0]); err != nil {
		return "", fmt.Errorf("%s: %w", d.Name(), err)
	}
	return scope.Schemas[0], nil
}

// readSchema reads the whole schema in complete scope, otherwise the declared
// tables and every table their foreign keys reach, so the snapshot is
// self-contained. The extra tables stay out of the projection.
func (d *Dialect) readSchema(ctx context.Context, drv atlas.Driver, name string, scope engine.Scope) (*schema.Schema, error) {
	opts := &schema.InspectOptions{Mode: tableMode}
	if scope.Mode == engine.ScopeDeclared {
		for _, o := range scope.Objects {
			opts.Tables = append(opts.Tables, o.Name)
		}
		if len(opts.Tables) == 0 {
			opts.Mode = schema.InspectSchemas | schema.InspectTypes
		}
	}
	s, err := drv.InspectSchema(ctx, name, opts)
	if err != nil {
		return nil, fmt.Errorf("%s: inspect schema %q: %w", d.Name(), name, err)
	}
	if scope.Mode == engine.ScopeDeclared {
		if err = d.closeForeignKeys(ctx, drv, s); err != nil {
			return nil, err
		}
	}
	return s, Exclude(s, excludedPatterns(ctx))
}

func (d *Dialect) closeForeignKeys(ctx context.Context, drv atlas.Driver, s *schema.Schema) error {
	tried := map[string]bool{}
	for {
		var missing []string
		for _, t := range s.Tables {
			for _, fk := range t.ForeignKeys {
				n := fk.RefTable.Name
				if _, ok := s.Table(n); !ok && sameSchema(fk, s.Name) && !tried[n] && !slices.Contains(missing, n) {
					missing = append(missing, n)
				}
			}
		}
		if len(missing) == 0 {
			return nil
		}
		for _, n := range missing {
			tried[n] = true
		}
		more, err := drv.InspectSchema(ctx, s.Name, &schema.InspectOptions{Mode: schema.InspectSchemas | schema.InspectTables, Tables: missing})
		if err != nil {
			return fmt.Errorf("%s: inspect foreign key targets %v: %w", d.Name(), missing, err)
		}
		s.AddTables(more.Tables...)
	}
}

func sameSchema(fk *schema.ForeignKey, name string) bool {
	return fk.RefTable.Schema == nil || fk.RefTable.Schema.Name == "" || fk.RefTable.Schema.Name == name
}

// assemble refuses what cannot be managed, encodes the rest as Native and
// projects the part inside the scope.
func (d *Dialect) assemble(s *schema.Schema, extras Extras, sc scoped) (engine.Inspection, error) {
	r := newRefuser(s)
	var refusals []engine.Refusal
	for _, x := range extras.Refusals {
		if x.Object.Schema == s.Name && r.isTable(x.Object.Name) && x.Member == "" {
			r.refuse(x.Object.Name, x.Reason)
			continue
		}
		refusals = append(refusals, x)
	}
	r.refuseCrossSchema()
	native, err := d.encodeRefusing(s, r)
	if err != nil {
		return engine.Inspection{}, err
	}
	tables, enums := map[string]bool{}, map[string]bool{}
	for _, t := range s.Tables {
		tables[t.Name] = sc.has(migrate.Name{Schema: s.Name, Name: t.Name})
	}
	for _, o := range s.Objects {
		if e, ok := o.(*schema.EnumType); ok {
			enums[e.T] = sc.has(migrate.Name{Schema: s.Name, Name: e.T})
		}
	}
	proj, err := d.project(s, tables, enums)
	if err != nil {
		return engine.Inspection{}, err
	}
	proj.Native = native
	for _, def := range extras.Definitions {
		if sc.has(def.Name) {
			proj.Definitions = append(proj.Definitions, def)
		}
	}
	for table, reason := range r.reasons {
		refusals = append(refusals, engine.Refusal{Object: migrate.Name{Schema: s.Name, Name: table}, Reason: reason})
	}
	refusals = slices.DeleteFunc(refusals, func(x engine.Refusal) bool { return !sc.has(x.Object) })
	return engine.Inspection{Schema: proj, Refusals: refusals}, nil
}

// encodeRefusing encodes s, refusing the tables whose round trip is lossy and
// removing every refused table, until the snapshot is lossless.
func (d *Dialect) encodeRefusing(s *schema.Schema, r *refuser) (string, error) {
	for {
		r.apply()
		native, err := d.encode(s)
		var lossy *lossyError
		if !errors.As(err, &lossy) {
			return native, err
		}
		tables, err := d.lossyTables(lossy)
		if err != nil {
			return "", err
		}
		before := len(r.reasons)
		for _, t := range tables {
			r.refuse(t, "the table cannot be represented losslessly in the native snapshot")
		}
		if len(r.reasons) == before {
			return "", lossy
		}
	}
}

func (d *Dialect) lossyTables(lossy *lossyError) ([]string, error) {
	set, err := changeset.Describe(lossy.changes, d.backend.Changes())
	if err != nil {
		return nil, err
	}
	var tables []string
	for _, c := range set.Changes {
		if c.Kind == enumKind || c.Kind == "schema" {
			return nil, fmt.Errorf("%s: %w: %s %s changes on a round trip", d.Name(), lossy, c.Kind, c.Object.Name)
		}
		if !slices.Contains(tables, c.Object.Name) {
			tables = append(tables, c.Object.Name)
		}
	}
	return tables, nil
}

// refuser tracks the tables that cannot be managed and the reason for each.
type refuser struct {
	s       *schema.Schema
	reasons map[string]string
}

func newRefuser(s *schema.Schema) *refuser { return &refuser{s: s, reasons: map[string]string{}} }

func (r *refuser) isTable(name string) bool { _, ok := r.s.Table(name); return ok }

// refuse records the table and, transitively, every table whose foreign keys reach it.
func (r *refuser) refuse(table, reason string) {
	if _, ok := r.reasons[table]; ok {
		return
	}
	r.reasons[table] = reason
	for _, t := range r.s.Tables {
		for _, fk := range t.ForeignKeys {
			if fk.RefTable.Name == table && sameSchema(fk, r.s.Name) && t.Name != table {
				r.refuse(t.Name, fmt.Sprintf("foreign key %q references refused table %q: %s", fk.Symbol, table, reason))
			}
		}
	}
}

func (r *refuser) refuseCrossSchema() {
	for _, t := range r.s.Tables {
		for _, fk := range t.ForeignKeys {
			if !sameSchema(fk, r.s.Name) {
				r.refuse(t.Name, fmt.Sprintf("foreign key %q references schema %q", fk.Symbol, fk.RefTable.Schema.Name))
			}
		}
	}
}

func (r *refuser) apply() {
	r.s.Tables = slices.DeleteFunc(r.s.Tables, func(t *schema.Table) bool { _, ok := r.reasons[t.Name]; return ok })
}
