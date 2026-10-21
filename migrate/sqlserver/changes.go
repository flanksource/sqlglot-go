package sqlserver

import (
	"encoding/json"
	"fmt"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
)

func describe(c schema.Change, table *schema.Table, metadata map[schema.Change]migrate.Change, changes *[]migrate.Change) error {
	if modify, ok := c.(*schema.ModifyTable); ok {
		for _, child := range modify.Changes {
			if err := describe(child, modify.T, metadata, changes); err != nil {
				return err
			}
		}
		return nil
	}
	change, err := changeOf(c, table)
	if err != nil {
		return err
	}
	id, err := json.Marshal([]string{change.Action, change.Kind, change.Object.Schema, change.Object.Name, change.Member})
	if err != nil {
		return err
	}
	change.ID = string(id)
	metadata[c] = change
	*changes = append(*changes, change)
	return nil
}

func changeOf(c schema.Change, table *schema.Table) (migrate.Change, error) {
	change := migrate.Change{}
	if table != nil {
		change.Object = tableName(table)
	}
	switch c := c.(type) {
	case *schema.AddTable:
		change.Action, change.Kind, change.Object = "add", "table", tableName(c.T)
	case *schema.DropTable:
		change.Action, change.Kind, change.Object = "drop", "table", tableName(c.T)
	case *schema.AddColumn:
		change.Action, change.Kind, change.Member = "add", "column", c.C.Name
	case *schema.DropColumn:
		change.Action, change.Kind, change.Member = "drop", "column", c.C.Name
	case *schema.ModifyColumn:
		change.Action, change.Kind, change.Member = "modify", "column", c.To.Name
		if c.Change.Is(schema.ChangeType) || c.Change.Is(schema.ChangeAttr) || c.From.Type.Null && !c.To.Type.Null {
			change.Destructive, change.Reason = true, "Column type, collation or nullability change may reject or lose existing data"
		}
	case *schema.AddIndex:
		change.Action, change.Kind, change.Member, change.Object = "add", "index", c.I.Name, tableName(c.I.Table)
	case *schema.DropIndex:
		change.Action, change.Kind, change.Member, change.Object = "drop", "index", c.I.Name, tableName(c.I.Table)
	case *schema.ModifyIndex:
		change.Action, change.Kind, change.Member, change.Object = "modify", "index", c.To.Name, tableName(c.To.Table)
	case *schema.AddPrimaryKey:
		change.Action, change.Kind, change.Member = "add", "primary-key", c.P.Name
	case *schema.DropPrimaryKey:
		change.Action, change.Kind, change.Member = "drop", "primary-key", c.P.Name
	case *schema.ModifyPrimaryKey:
		change.Action, change.Kind, change.Member = "modify", "primary-key", c.To.Name
	case *schema.AddForeignKey:
		change.Action, change.Kind, change.Member, change.Object = "add", "foreign-key", c.F.Symbol, tableName(c.F.Table)
	case *schema.DropForeignKey:
		change.Action, change.Kind, change.Member, change.Object = "drop", "foreign-key", c.F.Symbol, tableName(c.F.Table)
	case *schema.ModifyForeignKey:
		change.Action, change.Kind, change.Member, change.Object = "modify", "foreign-key", c.To.Symbol, tableName(c.To.Table)
	case *schema.AddCheck:
		change.Action, change.Kind, change.Member = "add", "check", c.C.Name
	case *schema.DropCheck:
		change.Action, change.Kind, change.Member = "drop", "check", c.C.Name
	case *schema.ModifyCheck:
		change.Action, change.Kind, change.Member = "modify", "check", c.To.Name
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
	default:
		return change, fmt.Errorf("sqlserver: cannot describe change %T", c)
	}
	if change.Object.Schema == "" || change.Object.Name == "" {
		return change, fmt.Errorf("sqlserver: change %T has no owning object", c)
	}
	if change.Action == "drop" {
		change.Destructive, change.Reason = true, "Drops an existing database object or member"
	}
	return change, nil
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

func tableName(table *schema.Table) migrate.Name {
	if table == nil || table.Schema == nil {
		return migrate.Name{}
	}
	return migrate.Name{Schema: table.Schema.Name, Name: table.Name}
}
