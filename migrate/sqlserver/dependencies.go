package sqlserver

import (
	"fmt"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
)

func dependencies(r *schema.Realm, definitions []migrate.Definition) error {
	objects := make(map[migrate.Name]schema.Object)
	for _, s := range r.Schemas {
		for _, t := range s.Tables {
			objects[migrate.Name{Schema: s.Name, Name: t.Name}] = t
		}
		for _, v := range s.Views {
			objects[migrate.Name{Schema: s.Name, Name: v.Name}] = v
		}
		for _, f := range s.Funcs {
			objects[migrate.Name{Schema: s.Name, Name: f.Name}] = f
		}
		for _, p := range s.Procs {
			objects[migrate.Name{Schema: s.Name, Name: p.Name}] = p
		}
	}
	for _, d := range definitions {
		var deps []schema.Object
		for _, name := range d.Dependencies {
			object, ok := objects[name]
			if !ok {
				return fmt.Errorf("sqlserver: %s.%s: missing dependency %s.%s", d.Name.Schema, d.Name.Name, name.Schema, name.Name)
			}
			deps = append(deps, object)
		}
		switch o := objects[d.Name].(type) {
		case *schema.View:
			o.AddDeps(deps...)
		case *schema.Func:
			o.AddDeps(deps...)
		case *schema.Proc:
			o.AddDeps(deps...)
		default:
			if d.Kind != "TRIGGER" || d.Parent == nil {
				return fmt.Errorf("sqlserver: definition %s.%s has no object", d.Name.Schema, d.Name.Name)
			}
			parent := objects[*d.Parent]
			var trigger *schema.Trigger
			switch p := parent.(type) {
			case *schema.Table:
				trigger, _ = p.Trigger(d.Name.Name)
			case *schema.View:
				trigger, _ = p.Trigger(d.Name.Name)
			}
			if trigger == nil {
				return fmt.Errorf("sqlserver: trigger %s has no parent", d.Name.Name)
			}
			trigger.Deps = append(deps, parent)
		}
	}
	return nil
}
