package sqlserver

import (
	"fmt"

	"ariga.io/atlas/sql/schema"
)

func (*diff) TriggerDiff(from, to *schema.Trigger) ([]schema.Change, error) {
	same, err := sameDefinition(to.Name, from.Attrs, to.Attrs)
	if err != nil {
		return nil, err
	}
	_, before := attrOf[*Disabled](from.Attrs)
	_, after := attrOf[*Disabled](to.Attrs)
	if same && before == after {
		return nil, nil
	}
	return []schema.Change{&schema.ModifyTrigger{From: from, To: to}}, nil
}

func (s *state) moduleChange(change schema.Change) error {
	switch c := change.(type) {
	case *schema.AddView:
		return s.createOrAlter(c, c.V.Name, c.V.Attrs)
	case *schema.ModifyView:
		return s.createOrAlter(c, c.To.Name, c.To.Attrs)
	case *schema.DropView:
		s.append(c, fmt.Sprintf("drop view %q", c.V.Name), s.build("DROP VIEW").View(c.V))
	case *schema.AddTrigger:
		return s.trigger(c, c.T)
	case *schema.ModifyTrigger:
		return s.trigger(c, c.To)
	case *schema.DropTrigger:
		parent := triggerParent(c.T)
		if parent == nil {
			return fmt.Errorf("sqlserver: trigger %s has no parent", c.T.Name)
		}
		s.append(c, fmt.Sprintf("drop trigger %q", c.T.Name), s.build("DROP TRIGGER").Table(&schema.Table{Name: c.T.Name, Schema: parent.Schema}))
	default:
		return fmt.Errorf("sqlserver: change %T is %w", c, errUnsupported)
	}
	return nil
}

func (s *state) trigger(change schema.Change, trigger *schema.Trigger) error {
	parent := triggerParent(trigger)
	if parent == nil {
		return fmt.Errorf("sqlserver: trigger %s has no parent", trigger.Name)
	}
	if err := s.createOrAlter(change, trigger.Name, trigger.Attrs); err != nil {
		return err
	}
	verb := "ENABLE TRIGGER"
	if _, ok := attrOf[*Disabled](trigger.Attrs); ok {
		verb = "DISABLE TRIGGER"
	}
	s.append(change, fmt.Sprintf("set trigger %q state", trigger.Name), s.build(verb).Table(&schema.Table{Name: trigger.Name, Schema: parent.Schema}).P("ON").Table(parent))
	return nil
}

func triggerParent(t *schema.Trigger) *schema.Table {
	if t.Table != nil {
		return t.Table
	}
	if t.View != nil {
		return &schema.Table{Name: t.View.Name, Schema: t.View.Schema}
	}
	return nil
}
