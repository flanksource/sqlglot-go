package atlasdriver

import (
	"fmt"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
)

// SchemaFromHCL turns declared Atlas HCL into the schema the engine plans
// toward: canonical Native plus the projection of every table and enum type.
func (d *Dialect) SchemaFromHCL(hcl string) (migrate.Schema, error) {
	s, err := d.decode(hcl)
	if err != nil {
		return migrate.Schema{}, err
	}
	return d.SchemaFromAtlas(s)
}

// SchemaFromAtlas is SchemaFromHCL for a schema the caller already evaluated
// (with variables, remapped or projected to the dialect). It refuses what the
// engine does not manage instead of dropping it from the snapshot.
func (d *Dialect) SchemaFromAtlas(s *schema.Schema) (migrate.Schema, error) {
	if err := d.refuseUnmanaged(s); err != nil {
		return migrate.Schema{}, err
	}
	native, err := d.encode(s)
	if err != nil {
		return migrate.Schema{}, fmt.Errorf("%s: declared schema %q: %w", d.Name(), s.Name, err)
	}
	tables, enums := map[string]bool{}, map[string]bool{}
	for _, t := range s.Tables {
		tables[t.Name] = true
	}
	for _, o := range s.Objects {
		if e, ok := o.(*schema.EnumType); ok {
			enums[e.T] = true
		}
	}
	proj, err := d.project(s, tables, enums)
	if err != nil {
		return migrate.Schema{}, err
	}
	proj.Native = native
	return proj, nil
}

// refuseUnmanaged fails on a declared object the planner has no change model
// for: it would otherwise be marshalled into Native and never created.
func (d *Dialect) refuseUnmanaged(s *schema.Schema) error {
	if s == nil {
		return fmt.Errorf("%s: declared schema is nil", d.Name())
	}
	if n := len(s.Views) + len(s.Funcs) + len(s.Procs); n > 0 {
		return fmt.Errorf("%s: schema %q declares %d views, functions or procedures; declare them in SQL scripts", d.Name(), s.Name, n)
	}
	if s.Realm != nil && len(s.Realm.Objects) > 0 {
		return fmt.Errorf("%s: declared realm holds %d objects (extensions) the planner does not manage", d.Name(), len(s.Realm.Objects))
	}
	for _, o := range s.Objects {
		if _, ok := o.(*schema.EnumType); !ok {
			return fmt.Errorf("%s: schema %q declares a %T, which the planner does not manage", d.Name(), s.Name, o)
		}
	}
	return nil
}
