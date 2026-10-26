// Package changeset names atlas schema changes as selectable, typed migration
// changes and filters a diff down to a reviewed selection before planning.
package changeset

import (
	"encoding/json"
	"fmt"
	"strings"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
)

// Dialect supplies the parts of a change description that differ per backend.
type Dialect struct {
	// Name prefixes errors, e.g. "sqlserver".
	Name string
	// DefaultSchema names objects whose atlas schema is unset (SQLite: main).
	DefaultSchema string
	// Object names a dialect-specific schema object; ok is false for an unknown one.
	Object func(schema.Object) (kind string, name migrate.Name, ok bool)
	// Destructive reports whether a described change may lose data, and why.
	Destructive func(schema.Change) (bool, string)
	// Rebuilds reports whether a table member change forces the whole table to
	// be rebuilt from its desired definition, which applies every sibling change.
	Rebuilds func(schema.Change) bool
}

func (d Dialect) describe(c schema.Change, table *schema.Table) (migrate.Change, error) {
	change, err := d.changeOf(c, table)
	if err != nil {
		return change, err
	}
	if change.Object.Schema == "" || change.Object.Name == "" {
		return change, fmt.Errorf("%s: change %T has no owning object", d.Name, c)
	}
	id, err := json.Marshal([]string{change.Action, change.Kind, change.Object.Schema, change.Object.Name, change.Member})
	if err != nil {
		return change, err
	}
	change.ID = string(id)
	if d.Destructive != nil {
		change.Destructive, change.Reason = d.Destructive(c)
	}
	return change, nil
}

func (d Dialect) changeOf(c schema.Change, table *schema.Table) (migrate.Change, error) {
	change := migrate.Change{}
	if table != nil {
		change.Object = d.tableName(table)
	}
	switch c := c.(type) {
	case *schema.AddTable:
		change.Action, change.Kind, change.Object = "add", "table", d.tableName(c.T)
	case *schema.DropTable:
		change.Action, change.Kind, change.Object = "drop", "table", d.tableName(c.T)
	case *schema.AddColumn:
		change.Action, change.Kind, change.Member = "add", "column", c.C.Name
	case *schema.DropColumn:
		change.Action, change.Kind, change.Member = "drop", "column", c.C.Name
	case *schema.ModifyColumn:
		change.Action, change.Kind, change.Member = "modify", "column", c.To.Name
	case *schema.AddIndex:
		change.Action, change.Kind, change.Member = "add", "index", c.I.Name
		change.Object = d.ownerName(c.I.Table, change.Object)
	case *schema.DropIndex:
		change.Action, change.Kind, change.Member = "drop", "index", c.I.Name
		change.Object = d.ownerName(c.I.Table, change.Object)
	case *schema.ModifyIndex:
		change.Action, change.Kind, change.Member = "modify", "index", c.To.Name
		change.Object = d.ownerName(c.To.Table, change.Object)
	case *schema.AddPrimaryKey:
		change.Action, change.Kind, change.Member = "add", "primary-key", c.P.Name
	case *schema.DropPrimaryKey:
		change.Action, change.Kind, change.Member = "drop", "primary-key", c.P.Name
	case *schema.ModifyPrimaryKey:
		change.Action, change.Kind, change.Member = "modify", "primary-key", c.To.Name
	case *schema.AddForeignKey:
		change.Action, change.Kind, change.Member = "add", "foreign-key", c.F.Symbol
		change.Object = d.ownerName(c.F.Table, change.Object)
	case *schema.DropForeignKey:
		change.Action, change.Kind, change.Member = "drop", "foreign-key", c.F.Symbol
		change.Object = d.ownerName(c.F.Table, change.Object)
	case *schema.ModifyForeignKey:
		change.Action, change.Kind, change.Member = "modify", "foreign-key", c.To.Symbol
		change.Object = d.ownerName(c.To.Table, change.Object)
	case *schema.AddCheck:
		change.Action, change.Kind, change.Member = "add", "check", c.C.Name
	case *schema.DropCheck:
		change.Action, change.Kind, change.Member = "drop", "check", c.C.Name
	case *schema.ModifyCheck:
		change.Action, change.Kind, change.Member = "modify", "check", c.To.Name
	case *schema.AddAttr:
		change.Action, change.Kind, change.Member = "add", "attribute", attrName(c.A)
	case *schema.ModifyAttr:
		change.Action, change.Kind, change.Member = "modify", "attribute", attrName(c.To)
	case *schema.DropAttr:
		change.Action, change.Kind, change.Member = "drop", "attribute", attrName(c.A)
	case *schema.ModifySchema:
		change.Action, change.Kind, change.Object = "modify", "schema", migrate.Name{Schema: c.S.Name, Name: c.S.Name}
	case *schema.AddProc:
		change.Action, change.Kind, change.Object = "add", "procedure", migrate.Name{Schema: c.P.Schema.Name, Name: c.P.Name}
	case *schema.ModifyProc:
		change.Action, change.Kind, change.Object = "modify", "procedure", migrate.Name{Schema: c.To.Schema.Name, Name: c.To.Name}
	case *schema.DropProc:
		change.Action, change.Kind, change.Object = "drop", "procedure", migrate.Name{Schema: c.P.Schema.Name, Name: c.P.Name}
	case *schema.AddFunc:
		change.Action, change.Kind, change.Object = "add", "function", migrate.Name{Schema: c.F.Schema.Name, Name: c.F.Name}
	case *schema.ModifyFunc:
		change.Action, change.Kind, change.Object = "modify", "function", migrate.Name{Schema: c.To.Schema.Name, Name: c.To.Name}
	case *schema.DropFunc:
		change.Action, change.Kind, change.Object = "drop", "function", migrate.Name{Schema: c.F.Schema.Name, Name: c.F.Name}
	case *schema.AddView:
		change.Action, change.Kind, change.Object = "add", "view", migrate.Name{Schema: c.V.Schema.Name, Name: c.V.Name}
	case *schema.ModifyView:
		change.Action, change.Kind, change.Object = "modify", "view", migrate.Name{Schema: c.To.Schema.Name, Name: c.To.Name}
	case *schema.DropView:
		change.Action, change.Kind, change.Object = "drop", "view", migrate.Name{Schema: c.V.Schema.Name, Name: c.V.Name}
	case *schema.AddTrigger:
		change.Action, change.Kind, change.Object = "add", "trigger", triggerName(c.T)
	case *schema.ModifyTrigger:
		change.Action, change.Kind, change.Object = "modify", "trigger", triggerName(c.To)
	case *schema.DropTrigger:
		change.Action, change.Kind, change.Object = "drop", "trigger", triggerName(c.T)
	case *schema.AddObject:
		change.Action = "add"
		return d.object(change, c.O)
	case *schema.ModifyObject:
		change.Action = "modify"
		return d.object(change, c.To)
	case *schema.DropObject:
		change.Action = "drop"
		return d.object(change, c.O)
	default:
		return change, fmt.Errorf("%s: cannot describe change %T", d.Name, c)
	}
	return change, nil
}

func (d Dialect) object(change migrate.Change, o schema.Object) (migrate.Change, error) {
	if d.Object == nil {
		return change, fmt.Errorf("%s: cannot describe schema object %T", d.Name, o)
	}
	kind, name, ok := d.Object(o)
	if !ok {
		return change, fmt.Errorf("%s: cannot describe schema object %T", d.Name, o)
	}
	change.Kind, change.Object = kind, name
	return change, nil
}

func (d Dialect) tableName(table *schema.Table) migrate.Name {
	if table == nil {
		return migrate.Name{}
	}
	if table.Schema == nil || table.Schema.Name == "" {
		return migrate.Name{Schema: d.DefaultSchema, Name: table.Name}
	}
	return migrate.Name{Schema: table.Schema.Name, Name: table.Name}
}

// ownerName prefers a member's own table, falling back to the modified table.
func (d Dialect) ownerName(table *schema.Table, fallback migrate.Name) migrate.Name {
	if name := d.tableName(table); name.Name != "" {
		return name
	}
	return fallback
}

func attrName(a schema.Attr) string {
	if _, ok := a.(*schema.Comment); ok {
		return "comment"
	}
	name := fmt.Sprintf("%T", a)
	return strings.ToLower(name[strings.LastIndex(name, ".")+1:])
}

func triggerName(t *schema.Trigger) migrate.Name {
	if t.Table != nil {
		return migrate.Name{Schema: t.Table.Schema.Name, Name: t.Name}
	}
	if t.View != nil {
		return migrate.Name{Schema: t.View.Schema.Name, Name: t.Name}
	}
	return migrate.Name{}
}
