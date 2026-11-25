package atlasdriver

import (
	"fmt"
	"slices"
	"strings"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
)

// project is the migrate.Schema view of the named tables and enum types of s:
// names, columns, indexes, foreign keys and checks. Native carries the rest.
func (d *Dialect) project(s *schema.Schema, tables, enums map[string]bool) (migrate.Schema, error) {
	out := migrate.Schema{Tables: []migrate.Table{}, Definitions: []migrate.Definition{}}
	for _, t := range s.Tables {
		if !tables[t.Name] {
			continue
		}
		pt, err := d.projectTable(s.Name, t)
		if err != nil {
			return out, err
		}
		out.Tables = append(out.Tables, pt)
	}
	for _, o := range s.Objects {
		if e, ok := o.(*schema.EnumType); ok && enums[e.T] {
			out.Definitions = append(out.Definitions, migrate.Definition{
				Name: migrate.Name{Schema: s.Name, Name: e.T}, Kind: enumKind, SQL: strings.Join(e.Values, ","),
			})
		}
	}
	slices.SortFunc(out.Tables, func(a, b migrate.Table) int { return strings.Compare(a.Name.Name, b.Name.Name) })
	slices.SortFunc(out.Definitions, func(a, b migrate.Definition) int { return strings.Compare(a.Name.Name, b.Name.Name) })
	return out, nil
}

func (d *Dialect) projectTable(schemaName string, t *schema.Table) (migrate.Table, error) {
	pt := migrate.Table{Name: migrate.Name{Schema: schemaName, Name: t.Name}}
	for _, c := range t.Columns {
		typ, err := d.atlas.FormatType(c.Type.Type)
		if err != nil {
			return pt, fmt.Errorf("%s: format type of %s.%s: %w", d.Name(), t.Name, c.Name, err)
		}
		pt.Columns = append(pt.Columns, migrate.Column{Name: c.Name, Type: typ, Nullable: c.Type.Null, Default: columnDefault(c)})
	}
	if t.PrimaryKey != nil {
		pk := d.projectIndex(t.PrimaryKey)
		pk.Primary, pk.Unique, pk.Constraint = true, true, true
		pt.Indexes = append(pt.Indexes, pk)
	}
	for _, i := range t.Indexes {
		pt.Indexes = append(pt.Indexes, d.projectIndex(i))
	}
	for _, fk := range t.ForeignKeys {
		pt.ForeignKeys = append(pt.ForeignKeys, projectForeignKey(schemaName, fk))
	}
	for _, c := range t.Checks() {
		pt.Checks = append(pt.Checks, migrate.Check{Name: c.Name, Expression: c.Expr})
	}
	return pt, nil
}

func columnDefault(c *schema.Column) *migrate.Default {
	switch x := c.Default.(type) {
	case *schema.Literal:
		return &migrate.Default{Expression: x.V}
	case *schema.RawExpr:
		return &migrate.Default{Expression: x.X}
	}
	return nil
}

func (d *Dialect) projectIndex(i *schema.Index) migrate.Index {
	out := migrate.Index{Name: i.Name, Unique: i.Unique, Filter: d.atlas.IndexFilter(i.Attrs)}
	for _, p := range i.Parts {
		switch {
		case p.C != nil:
			out.Columns = append(out.Columns, migrate.IndexColumn{Name: p.C.Name, Descending: p.Desc})
		case p.X != nil:
			out.Columns = append(out.Columns, migrate.IndexColumn{Name: exprText(p.X), Descending: p.Desc})
		}
	}
	return out
}

func exprText(x schema.Expr) string {
	switch x := x.(type) {
	case *schema.RawExpr:
		return x.X
	case *schema.Literal:
		return x.V
	}
	return fmt.Sprint(x)
}

func projectForeignKey(schemaName string, fk *schema.ForeignKey) migrate.ForeignKey {
	out := migrate.ForeignKey{Name: fk.Symbol, OnDelete: string(fk.OnDelete), OnUpdate: string(fk.OnUpdate)}
	for _, c := range fk.Columns {
		out.Columns = append(out.Columns, c.Name)
	}
	out.References = migrate.Name{Schema: schemaName, Name: fk.RefTable.Name}
	if fk.RefTable.Schema != nil && fk.RefTable.Schema.Name != "" {
		out.References.Schema = fk.RefTable.Schema.Name
	}
	for _, c := range fk.RefColumns {
		out.ReferencedColumns = append(out.ReferencedColumns, c.Name)
	}
	return out
}
