package sqlserver

import (
	"ariga.io/atlas/sql/schema"
	"fmt"
	"github.com/flanksource/sqlglot-go/migrate"
	atlas "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
)

// FromAtlas projects supported declarative SQL Server tables and routines into the native model.
func FromAtlas(input *schema.Realm) (migrate.Schema, error) {
	var output migrate.Schema
	if input == nil || len(input.Objects)+len(input.Attrs) > 0 {
		return output, fmt.Errorf("unsupported Atlas realm")
	}
	for _, s := range input.Schemas {
		if len(s.Objects)+len(s.Views)+len(s.Attrs) > 0 {
			return output, fmt.Errorf("unsupported Atlas schema objects in %q", s.Name)
		}
		for _, t := range s.Tables {
			table, err := fromAtlasTable(t)
			if err != nil {
				return output, err
			}
			output.Tables = append(output.Tables, table)
		}
		for _, p := range s.Procs {
			d, err := fromAtlasDefinition(migrate.Name{Schema: s.Name, Name: p.Name}, "PROCEDURE", p.Attrs, p.Deps)
			if err != nil {
				return output, err
			}
			output.Definitions = append(output.Definitions, d)
		}
		for _, f := range s.Funcs {
			d, err := fromAtlasDefinition(migrate.Name{Schema: s.Name, Name: f.Name}, "FUNCTION", f.Attrs, f.Deps)
			if err != nil {
				return output, err
			}
			output.Definitions = append(output.Definitions, d)
		}
	}
	_, err := Plan(migrate.Request{Current: output, Desired: output})
	return output, err
}

func fromAtlasTable(t *schema.Table) (migrate.Table, error) {
	if t.Schema == nil || len(t.Triggers) > 0 {
		return migrate.Table{}, fmt.Errorf("unsupported or unscoped table %q", t.Name)
	}
	result := migrate.Table{Name: migrate.Name{Schema: t.Schema.Name, Name: t.Name}}
	for _, c := range t.Columns {
		if c.Type == nil || c.Type.Type == nil {
			return result, fmt.Errorf("column %s has no parsed type", c.Name)
		}
		typ, err := atlas.FormatType(c.Type.Type)
		if err != nil {
			return result, err
		}
		column := migrate.Column{Name: c.Name, Type: typ, Nullable: c.Type.Null}
		if c.Default != nil {
			column.Default = &migrate.Default{}
			column.Default.Expression, err = atlas.DefaultSQL(c)
			if err != nil {
				return result, err
			}
		}
		for _, a := range c.Attrs {
			switch a := a.(type) {
			case *atlas.Identity:
				column.Identity = &migrate.Identity{Seed: a.Seed, Increment: a.Increment}
			case *atlas.DefaultConstraint:
				if column.Default == nil {
					return result, fmt.Errorf("default constraint without expression")
				}
				column.Default.Name = a.Name
			case *schema.Collation:
				column.Collation = a.V
			default:
				return result, fmt.Errorf("unsupported column attribute %T", a)
			}
		}
		result.Columns = append(result.Columns, column)
	}
	indexes := append([]*schema.Index(nil), t.Indexes...)
	if t.PrimaryKey != nil {
		indexes = append(indexes, t.PrimaryKey)
	}
	for _, i := range indexes {
		index, err := fromAtlasIndex(i, i == t.PrimaryKey)
		if err != nil {
			return result, err
		}
		if index.Primary && index.Name == "" {
			index.Name = "PK_" + t.Name
		}
		result.Indexes = append(result.Indexes, index)
	}
	for _, f := range t.ForeignKeys {
		if f.RefTable == nil || f.RefTable.Schema == nil || len(f.Attrs) > 0 {
			return result, fmt.Errorf("unsupported foreign key %s", f.Symbol)
		}
		key := migrate.ForeignKey{Name: f.Symbol, References: migrate.Name{Schema: f.RefTable.Schema.Name, Name: f.RefTable.Name}, OnDelete: string(f.OnDelete), OnUpdate: string(f.OnUpdate)}
		for _, c := range f.Columns {
			key.Columns = append(key.Columns, c.Name)
		}
		for _, c := range f.RefColumns {
			key.ReferencedColumns = append(key.ReferencedColumns, c.Name)
		}
		result.ForeignKeys = append(result.ForeignKeys, key)
	}
	for _, a := range t.Attrs {
		check, ok := a.(*schema.Check)
		if !ok || len(check.Attrs) > 0 {
			return result, fmt.Errorf("unsupported table attribute %T", a)
		}
		result.Checks = append(result.Checks, migrate.Check{Name: check.Name, Expression: check.Expr})
	}
	return result, nil
}

func fromAtlasIndex(i *schema.Index, primary bool) (migrate.Index, error) {
	result := migrate.Index{Name: i.Name, Unique: i.Unique, Primary: primary}
	for _, p := range i.Parts {
		if p.C == nil || p.X != nil || len(p.Attrs) > 0 {
			return result, fmt.Errorf("unsupported index part on %s", i.Name)
		}
		result.Columns = append(result.Columns, migrate.IndexColumn{Name: p.C.Name, Descending: p.Desc})
	}
	for _, a := range i.Attrs {
		switch a := a.(type) {
		case *atlas.Clustered:
			result.Clustered = true
		case *atlas.UniqueConstraint:
			result.Constraint = true
		case *atlas.IndexInclude:
			result.Included = append([]string(nil), a.Columns...)
		case *atlas.IndexPredicate:
			result.Filter = a.P
		default:
			return result, fmt.Errorf("unsupported index attribute %T", a)
		}
	}
	return result, nil
}

func fromAtlasDefinition(name migrate.Name, kind string, attrs []schema.Attr, deps []schema.Object) (migrate.Definition, error) {
	result := migrate.Definition{Name: name, Kind: kind}
	for _, a := range attrs {
		if definition, ok := a.(*atlas.Definition); ok {
			result.SQL = definition.Text
		} else {
			return result, fmt.Errorf("unsupported routine attribute %T", a)
		}
	}
	if result.SQL == "" {
		return result, fmt.Errorf("routine %s requires its SQL definition", name.Name)
	}
	for _, dependency := range deps {
		switch d := dependency.(type) {
		case *schema.Table:
			result.Dependencies = append(result.Dependencies, migrate.Name{Schema: d.Schema.Name, Name: d.Name})
		case *schema.Proc:
			result.Dependencies = append(result.Dependencies, migrate.Name{Schema: d.Schema.Name, Name: d.Name})
		case *schema.Func:
			result.Dependencies = append(result.Dependencies, migrate.Name{Schema: d.Schema.Name, Name: d.Name})
		default:
			return result, fmt.Errorf("unsupported routine dependency %T", dependency)
		}
	}
	return result, nil
}
