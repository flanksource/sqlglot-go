package atlasdriver

import (
	"strings"

	"ariga.io/atlas/sql/postgres"
	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/changeset"
)

const enumKind = "enum"

// enumOf returns the enum type a column type is, or is an array of.
func enumOf(t schema.Type) *schema.EnumType {
	switch t := t.(type) {
	case *schema.EnumType:
		return t
	case *postgres.ArrayType:
		return enumOf(t.Type)
	}
	return nil
}

// ObjectOf names the schema objects other than tables a change can act on.
func ObjectOf(o schema.Object) (string, migrate.Name, bool) {
	if e, ok := o.(*schema.EnumType); ok && e.Schema != nil {
		return enumKind, migrate.Name{Schema: e.Schema.Name, Name: e.T}, true
	}
	return "", migrate.Name{}, false
}

// DescribeDestructive is the destructive rule shared by every Atlas dialect.
func DescribeDestructive(c schema.Change) (bool, string) {
	if destructive(c) {
		return true, "May drop or rewrite existing data"
	}
	return false, ""
}

func destructive(change schema.Change) bool {
	switch c := change.(type) {
	case *schema.DropTable, *schema.DropColumn, *schema.DropObject, *schema.DropIndex, *schema.DropPrimaryKey, *schema.DropForeignKey, *schema.DropCheck:
		return true
	case *schema.ModifyColumn:
		return c.Change.Is(schema.ChangeType) || c.Change.Is(schema.ChangeNull)
	case *schema.ModifyTable:
		for _, inner := range c.Changes {
			if destructive(inner) {
				return true
			}
		}
	case *schema.ModifyObject, *schema.ModifyPrimaryKey, *schema.ModifyForeignKey, *schema.ModifyCheck:
		return true
	}
	return false
}

// BaseChanges is the changeset dialect every Atlas dialect starts from.
func BaseChanges(name string) changeset.Dialect {
	return changeset.Dialect{Name: name, Object: ObjectOf, Destructive: DescribeDestructive}
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
