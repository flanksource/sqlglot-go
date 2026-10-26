package review

import (
	"slices"
	"strings"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/changeset"
)

func (b backend) changeDialect() changeset.Dialect {
	d := changeset.Dialect{Name: b.dialect, Object: objectOf, Destructive: describeDestructive}
	if b.dialect == "sqlite" {
		d.DefaultSchema, d.Rebuilds = "main", rebuilds
	}
	return d
}

func describeDestructive(c schema.Change) (bool, string) {
	if destructive(c) {
		return true, "May drop or rewrite existing data"
	}
	return false, ""
}

func objectOf(o schema.Object) (string, migrate.Name, bool) {
	if e, ok := o.(*schema.EnumType); ok && e.Schema != nil {
		return "enum", migrate.Name{Schema: e.Schema.Name, Name: e.T}, true
	}
	return "", migrate.Name{}, false
}

// rebuilds mirrors atlas's SQLite alterable rule for a single member change:
// anything but index changes and a plain column addition rebuilds the table
// from its full desired definition, so it carries every sibling change along.
func rebuilds(c schema.Change) bool {
	switch c := c.(type) {
	case *schema.RenameColumn, *schema.RenameIndex, *schema.DropIndex, *schema.AddIndex:
		return false
	case *schema.AddColumn:
		if len(c.C.Indexes) > 0 || len(c.C.ForeignKeys) > 0 {
			return true
		}
		switch x := c.C.Default.(type) {
		case *schema.Literal:
			return slices.Contains([]string{"CURRENT_TIME", "CURRENT_DATE", "CURRENT_TIMESTAMP"}, x.V)
		case *schema.RawExpr:
			return true
		}
		return slices.ContainsFunc(c.C.Attrs, func(a schema.Attr) bool {
			g, ok := a.(*schema.GeneratedExpr)
			return ok && strings.EqualFold(g.Type, "STORED")
		})
	}
	return true
}

// unselected lists the change IDs a partial artifact leaves for later; once
// it is applied, they are exactly what still differs from desired.
func (a Artifact) unselected() []string {
	if len(a.Selection) == 0 {
		return []string{}
	}
	ids := []string{}
	for _, c := range a.Changes {
		if !slices.Contains(a.Selection, c.ID) {
			ids = append(ids, c.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

// remaining describes what still differs between current and desired.
func (b backend) remaining(current, desired *schema.Schema) ([]string, error) {
	diff, err := b.diff(current, desired)
	if err != nil {
		return nil, err
	}
	set, err := changeset.Describe(diff, b.changeDialect())
	if err != nil {
		return nil, err
	}
	ids := set.IDs()
	slices.Sort(ids)
	return ids, nil
}
