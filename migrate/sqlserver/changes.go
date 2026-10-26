package sqlserver

import (
	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
	atlas "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
	"github.com/flanksource/sqlglot-go/migrate/changeset"
)

var dialect = changeset.Dialect{Name: "sqlserver", Object: objectOf, Destructive: destructive}

func destructive(c schema.Change) (bool, string) {
	switch c := c.(type) {
	case *schema.DropTable, *schema.DropColumn, *schema.DropIndex, *schema.DropPrimaryKey, *schema.DropForeignKey, *schema.DropCheck,
		*schema.DropProc, *schema.DropFunc, *schema.DropView, *schema.DropTrigger, *schema.DropObject, *schema.DropAttr:
		return true, "Drops an existing database object or member"
	case *schema.ModifyColumn:
		if c.Change.Is(schema.ChangeType) || atlas.CollationChanged(c.From, c.To) || c.From.Type.Null && !c.To.Type.Null {
			return true, "Column type, collation or nullability change may reject or lose existing data"
		}
	}
	return false, ""
}

// requireDefinitionDependencies makes a module change need the creation of
// every object its definition depends on, when the same diff creates it.
func requireDefinitionDependencies(set *changeset.Set, desired migrate.Schema) {
	added := map[migrate.Name][]string{}
	for _, c := range set.Changes {
		if c.Action == "add" && c.Member == "" {
			added[c.Object] = append(added[c.Object], c.ID)
		}
	}
	dependencies := map[migrate.Name][]migrate.Name{}
	for _, d := range desired.Definitions {
		dependencies[d.Name] = d.Dependencies
	}
	for _, c := range set.Changes {
		if c.Action == "drop" || c.Member != "" {
			continue
		}
		for _, dependency := range dependencies[c.Object] {
			set.Require(c.ID, added[dependency]...)
		}
	}
}

// objectOf names a schema object change's kind and object.
func objectOf(o schema.Object) (string, migrate.Name, bool) {
	switch o := o.(type) {
	case *atlas.Sequence:
		if o.Schema != nil {
			return "sequence", migrate.Name{Schema: o.Schema.Name, Name: o.Name}, true
		}
	case *atlas.TableType:
		if o.Schema != nil {
			return "table-type", migrate.Name{Schema: o.Schema.Name, Name: o.Name}, true
		}
	}
	return "", migrate.Name{}, false
}
