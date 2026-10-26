package changeset

import (
	"fmt"
	"slices"
	"strings"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
)

// Set is the described diff: every selectable change in diff order, the
// atlas change each came from, and the table member changes per table.
type Set struct {
	Changes []migrate.Change
	dialect Dialect
	index   map[string]int
	sources map[schema.Change]string
	tables  map[string][]string
}

// Describe names every change of a diff, recursing into table modifications,
// and records which other changes each one requires.
func Describe(diff []schema.Change, d Dialect) (*Set, error) {
	s := &Set{Changes: []migrate.Change{}, dialect: d, index: map[string]int{}, sources: map[schema.Change]string{}, tables: map[string][]string{}}
	for _, c := range diff {
		if err := s.add(c, nil); err != nil {
			return nil, err
		}
	}
	s.requires(diff)
	return s, nil
}

func (s *Set) add(c schema.Change, table *schema.Table) error {
	if modify, ok := c.(*schema.ModifyTable); ok {
		for _, child := range modify.Changes {
			if err := s.add(child, modify.T); err != nil {
				return err
			}
		}
		return nil
	}
	change, err := s.dialect.describe(c, table)
	if err != nil {
		return err
	}
	if _, ok := s.index[change.ID]; ok {
		return fmt.Errorf("%s: duplicate change %s", s.dialect.Name, change.ID)
	}
	s.index[change.ID] = len(s.Changes)
	s.sources[c] = change.ID
	s.Changes = append(s.Changes, change)
	if table != nil {
		key := s.tableKey(table)
		s.tables[key] = append(s.tables[key], change.ID)
	}
	return nil
}

// Change returns the described change with the given ID.
func (s *Set) Change(id string) (migrate.Change, bool) {
	i, ok := s.index[id]
	if !ok {
		return migrate.Change{}, false
	}
	return s.Changes[i], true
}

// Require records that selecting id needs every one of required selected too.
func (s *Set) Require(id string, required ...string) {
	i, ok := s.index[id]
	if !ok {
		return
	}
	for _, r := range required {
		if r != id && !slices.Contains(s.Changes[i].Requires, r) {
			s.Changes[i].Requires = append(s.Changes[i].Requires, r)
		}
	}
	slices.Sort(s.Changes[i].Requires)
}

// IDs lists every change ID in diff order.
func (s *Set) IDs() []string {
	ids := make([]string, len(s.Changes))
	for i, c := range s.Changes {
		ids[i] = c.ID
	}
	return ids
}

// Select filters a diff to the selected change IDs, keeping a table
// modification with only its selected members. An empty selection keeps the
// whole diff. An unknown ID, or a selected change whose requirement is not
// selected, fails.
func (s *Set) Select(diff []schema.Change, selection []string) ([]schema.Change, error) {
	if len(selection) == 0 {
		return diff, nil
	}
	selected := map[string]bool{}
	for _, id := range selection {
		if _, ok := s.index[id]; !ok {
			return nil, fmt.Errorf("%s: selected change %s is not in the diff", s.dialect.Name, id)
		}
		selected[id] = true
	}
	for _, id := range selection {
		change, _ := s.Change(id)
		var missing []string
		for _, r := range change.Requires {
			if !selected[r] {
				missing = append(missing, r)
			}
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("%s: selected change %s requires unselected changes %s", s.dialect.Name, id, strings.Join(missing, ", "))
		}
	}
	var kept []schema.Change
	for _, c := range diff {
		modify, ok := c.(*schema.ModifyTable)
		if !ok {
			if selected[s.sources[c]] {
				kept = append(kept, c)
			}
			continue
		}
		var children []schema.Change
		for _, child := range modify.Changes {
			if selected[s.sources[child]] {
				children = append(children, child)
			}
		}
		if len(children) == len(modify.Changes) {
			kept = append(kept, modify)
		} else if len(children) > 0 {
			kept = append(kept, &schema.ModifyTable{T: modify.T, Changes: children})
		}
	}
	return kept, nil
}

// Lookup returns the change IDs a planned statement implements. Planners
// synthesize sources for table rebuilds and their recreated indexes; those
// resolve to the table's member changes.
func (s *Set) Lookup(source schema.Change) ([]string, error) {
	if modify, ok := source.(*schema.ModifyTable); ok {
		var ids []string
		for _, child := range modify.Changes {
			found, err := s.lookup(child, modify.T)
			if err != nil {
				return nil, err
			}
			ids = append(ids, found...)
		}
		if len(ids) == 0 {
			ids = s.tables[s.tableKey(modify.T)]
		}
		if len(ids) == 0 {
			return nil, fmt.Errorf("%s: planned table change %s maps to no described change", s.dialect.Name, s.tableKey(modify.T))
		}
		return slices.Compact(ids), nil
	}
	return s.lookup(source, nil)
}

// lookup matches a synthesized source by its description; failing that, a
// member of a created table belongs to the table's creation, and a member of
// a rebuilt table (or the temporary new_ table itself) to the table's changes.
func (s *Set) lookup(source schema.Change, table *schema.Table) ([]string, error) {
	if id, ok := s.sources[source]; ok {
		return []string{id}, nil
	}
	change, err := s.dialect.describe(source, table)
	if err != nil {
		return nil, err
	}
	if _, ok := s.index[change.ID]; ok {
		return []string{change.ID}, nil
	}
	owner := change.Object
	owner.Name = strings.TrimPrefix(owner.Name, "new_")
	if id := s.idOf("add", "table", owner, ""); s.has(id) {
		return []string{id}, nil
	}
	if ids := s.tables[owner.Schema+"."+owner.Name]; len(ids) > 0 {
		return ids, nil
	}
	return nil, fmt.Errorf("%s: planned statement source %s maps to no described change", s.dialect.Name, change.ID)
}

func (s *Set) has(id string) bool {
	_, ok := s.index[id]
	return ok
}

func (s *Set) tableKey(table *schema.Table) string {
	name := s.dialect.tableName(table)
	return name.Schema + "." + name.Name
}
