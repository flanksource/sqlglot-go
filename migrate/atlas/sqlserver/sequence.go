package sqlserver

import (
	"fmt"
	"slices"
	"strings"

	"ariga.io/atlas/sql/schema"

	"github.com/flanksource/sqlglot-go/migrate/internal/sqlx"
)

// Sequence is a CREATE SEQUENCE schema object. Bounds are exact decimal
// strings; CacheSize 0 with NoCache false is the server default cache.
type Sequence struct {
	schema.Object
	Schema                                *schema.Schema
	Name, Type, Start, Increment, Minimum string
	Maximum                               string
	Cycle, NoCache                        bool
	CacheSize                             int64
}

func sequencesOf(s *schema.Schema) ([]*Sequence, error) {
	var sequences []*Sequence
	for _, o := range s.Objects {
		switch o := o.(type) {
		case *Sequence:
			sequences = append(sequences, o)
		case *TableType:
		default:
			return nil, fmt.Errorf("sqlserver: schema %s: object %T is %w", s.Name, o, errUnsupported)
		}
	}
	return sequences, nil
}

func findSequence(sequences []*Sequence, name string) *Sequence {
	i := slices.IndexFunc(sequences, func(q *Sequence) bool { return strings.EqualFold(q.Name, name) })
	if i < 0 {
		return nil
	}
	return sequences[i]
}

// sequenceDiff adds, drops and alters sequences. The type and START WITH are
// fixed at creation and only a drop -- which resets the current value -- could
// change them, so such a change is refused.
func sequenceDiff(from, to *schema.Schema) ([]schema.Change, error) {
	current, err := sequencesOf(from)
	if err != nil {
		return nil, err
	}
	desired, err := sequencesOf(to)
	if err != nil {
		return nil, err
	}
	var changes []schema.Change
	for _, q := range desired {
		live := findSequence(current, q.Name)
		switch {
		case live == nil:
			changes = append(changes, &schema.AddObject{O: q})
		case !strings.EqualFold(live.Type, q.Type) || live.Start != q.Start:
			return nil, fmt.Errorf("sqlserver: sequence %s.%s: changing its type or START WITH is %w", to.Name, q.Name, errUnsupported)
		case live.Increment != q.Increment || live.Minimum != q.Minimum || live.Maximum != q.Maximum ||
			live.Cycle != q.Cycle || live.NoCache != q.NoCache || live.CacheSize != q.CacheSize:
			changes = append(changes, &schema.ModifyObject{From: live, To: q})
		}
	}
	for _, q := range current {
		if findSequence(desired, q.Name) == nil {
			changes = append(changes, &schema.DropObject{O: q})
		}
	}
	return changes, nil
}

func (s *state) sequenceChange(change schema.Change) (bool, error) {
	switch c := change.(type) {
	case *schema.AddObject:
		q, ok := c.O.(*Sequence)
		if !ok {
			return true, fmt.Errorf("sqlserver: object %T is %w", c.O, errUnsupported)
		}
		b := s.sequenceName(s.build("CREATE SEQUENCE"), q).P("AS", q.Type, "START WITH", q.Start)
		s.append(c, fmt.Sprintf("create sequence %q", q.Name), sequenceOptions(b, q))
	case *schema.ModifyObject:
		q, ok := c.To.(*Sequence)
		if !ok {
			return true, fmt.Errorf("sqlserver: object %T is %w", c.To, errUnsupported)
		}
		s.append(c, fmt.Sprintf("alter sequence %q", q.Name), sequenceOptions(s.sequenceName(s.build("ALTER SEQUENCE"), q), q))
	case *schema.DropObject:
		q, ok := c.O.(*Sequence)
		if !ok {
			return true, fmt.Errorf("sqlserver: object %T is %w", c.O, errUnsupported)
		}
		s.append(c, fmt.Sprintf("drop sequence %q", q.Name), s.sequenceName(s.build("DROP SEQUENCE"), q))
	default:
		return false, nil
	}
	return true, nil
}

func (s *state) sequenceName(b *sqlx.Builder, q *Sequence) *sqlx.Builder {
	return b.Table(&schema.Table{Name: q.Name, Schema: q.Schema})
}

func sequenceOptions(b *sqlx.Builder, q *Sequence) *sqlx.Builder {
	b.P("INCREMENT BY", q.Increment, "MINVALUE", q.Minimum, "MAXVALUE", q.Maximum)
	if q.Cycle {
		b.P("CYCLE")
	} else {
		b.P("NO CYCLE")
	}
	switch {
	case q.NoCache:
		b.P("NO CACHE")
	case q.CacheSize > 0:
		b.P(fmt.Sprintf("CACHE %d", q.CacheSize))
	default:
		b.P("CACHE")
	}
	return b
}
