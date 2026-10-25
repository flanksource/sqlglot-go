package sqlserver

import (
	"fmt"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
	atlas "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
	"github.com/flanksource/sqlglot-go/sqlglot"
)

func module(r *schema.Realm, s *schema.Schema, input migrate.Definition) error {
	text, err := atlas.CreateOrAlter(input.SQL)
	if err != nil {
		return err
	}
	parsed, err := sqlglot.ParseScript(text, "tsql")
	if err != nil {
		return err
	}
	if !parsed.Complete || parsed.Tree == nil || parsed.Tree.Class == "Block" {
		return fmt.Errorf("sqlserver: module %s requires one completely parsed definition", input.Name.Name)
	}
	tree := parsed.Tree
	if tree == nil || tree.Class != "Create" || tree.Args["kind"] != input.Kind {
		return fmt.Errorf("sqlserver: definition is not CREATE %s", input.Kind)
	}
	if definitionName(tree.This()) != input.Name {
		return fmt.Errorf("sqlserver: module must name its qualified catalog identifier %+v", input.Name)
	}
	attrs := []schema.Attr{&atlas.Definition{Text: input.SQL}}
	if input.QuotedIdentifierOff {
		attrs = append(attrs, &atlas.QuotedIdentifierOff{})
	}
	if input.Kind == "VIEW" {
		if input.Parent != nil || input.Disabled {
			return fmt.Errorf("sqlserver: view cannot have a trigger parent or disabled state")
		}
		if _, ok := s.View(input.Name.Name); ok {
			return fmt.Errorf("sqlserver: duplicate view %s", input.Name.Name)
		}
		canonical, err := sqlglot.CanonicalSQL(text, "tsql")
		if err != nil {
			return err
		}
		s.AddViews(&schema.View{Name: input.Name.Name, Def: canonical, Attrs: attrs})
		return nil
	}
	if input.Parent == nil || input.Parent.Schema != input.Name.Schema {
		return fmt.Errorf("sqlserver: trigger %s requires its parent in the same schema", input.Name.Name)
	}
	parent, _ := tree.Args["target"].(*sqlglot.Expression)
	if definitionName(parent) != *input.Parent {
		return fmt.Errorf("sqlserver: trigger %s ON does not match its catalog parent", input.Name.Name)
	}
	trigger := &schema.Trigger{Name: input.Name.Name, Attrs: attrs}
	if input.Disabled {
		trigger.Attrs = append(trigger.Attrs, &atlas.Disabled{})
	}
	return attachTrigger(r, trigger, *input.Parent)
}

func definitionName(e *sqlglot.Expression) migrate.Name {
	if e != nil && e.Class == "Schema" {
		e = e.This()
	}
	if e == nil || e.Class != "Table" || e.Args["catalog"] != nil {
		return migrate.Name{}
	}
	db, _ := e.Args["db"].(*sqlglot.Expression)
	if db == nil {
		return migrate.Name{}
	}
	return migrate.Name{Schema: db.Name(), Name: e.Name()}
}

func attachTrigger(r *schema.Realm, trigger *schema.Trigger, parent migrate.Name) error {
	s, ok := r.Schema(parent.Schema)
	if !ok {
		return fmt.Errorf("sqlserver: trigger %s: missing parent schema", trigger.Name)
	}
	if table, ok := s.Table(parent.Name); ok {
		if _, exists := table.Trigger(trigger.Name); exists {
			return fmt.Errorf("sqlserver: duplicate trigger %s", trigger.Name)
		}
		trigger.Table = table
		table.Triggers = append(table.Triggers, trigger)
		return nil
	}
	if view, ok := s.View(parent.Name); ok {
		if _, exists := view.Trigger(trigger.Name); exists {
			return fmt.Errorf("sqlserver: duplicate trigger %s", trigger.Name)
		}
		trigger.View = view
		view.Triggers = append(view.Triggers, trigger)
		return nil
	}
	return fmt.Errorf("sqlserver: trigger %s: missing parent %s.%s", trigger.Name, parent.Schema, parent.Name)
}
