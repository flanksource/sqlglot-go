package changeset

import (
	"encoding/json"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
)

// requires records the structural edges between changes of one diff: a new
// member needs its new table and columns, and a drop needs the drops of
// everything still referencing what it removes.
func (s *Set) requires(diff []schema.Change) {
	for _, c := range diff {
		switch c := c.(type) {
		case *schema.AddTable:
			for _, fk := range c.T.ForeignKeys {
				s.requireReferenced(s.sources[c], c.T, fk)
			}
		case *schema.DropTable:
			s.requireUnreferenced(s.sources[c], diff, c.T, nil)
		case *schema.ModifyTable:
			s.requiresMembers(diff, c)
		}
	}
}

func (s *Set) requiresMembers(diff []schema.Change, modify *schema.ModifyTable) {
	for _, child := range modify.Changes {
		id := s.sources[child]
		switch c := child.(type) {
		case *schema.AddForeignKey:
			s.requireColumns(id, modify.T, c.F.Columns...)
			s.requireReferenced(id, modify.T, c.F)
		case *schema.AddIndex:
			s.requireColumns(id, modify.T, partColumns(c.I)...)
		case *schema.AddPrimaryKey:
			s.requireColumns(id, modify.T, partColumns(c.P)...)
		case *schema.DropColumn:
			s.requireUnreferenced(id, diff, modify.T, c.C)
		}
		if s.dialect.Rebuilds != nil && s.dialect.Rebuilds(child) {
			for _, sibling := range modify.Changes {
				s.Require(id, s.sources[sibling])
			}
		}
	}
}

// requireReferenced makes a new foreign key need its referenced table and
// columns when the same diff creates them.
func (s *Set) requireReferenced(id string, owner *schema.Table, fk *schema.ForeignKey) {
	if fk.RefTable == nil || s.sameTable(fk.RefTable, owner) {
		return
	}
	s.requireIfPresent(id, s.idOf("add", "table", s.dialect.tableName(fk.RefTable), ""))
	s.requireColumns(id, fk.RefTable, fk.RefColumns...)
}

func (s *Set) requireColumns(id string, table *schema.Table, columns ...*schema.Column) {
	for _, column := range columns {
		if column != nil {
			s.requireIfPresent(id, s.idOf("add", "column", s.dialect.tableName(table), column.Name))
		}
	}
}

// requireUnreferenced makes dropping a table (column nil) or one of its
// columns need the drops of the keys, indexes and tables that reference it.
func (s *Set) requireUnreferenced(id string, diff []schema.Change, table *schema.Table, column *schema.Column) {
	for _, c := range diff {
		switch c := c.(type) {
		case *schema.DropTable:
			if s.sameTable(c.T, table) {
				continue
			}
			for _, fk := range c.T.ForeignKeys {
				if s.references(fk, table, column) {
					s.requireIfPresent(id, s.sources[c])
				}
			}
		case *schema.ModifyTable:
			for _, child := range c.Changes {
				switch m := child.(type) {
				case *schema.DropForeignKey:
					if s.references(m.F, table, column) || column != nil && s.sameTable(c.T, table) && hasColumn(m.F.Columns, column) {
						s.requireIfPresent(id, s.sources[child])
					}
				case *schema.DropIndex:
					if column != nil && s.sameTable(c.T, table) && hasColumn(partColumns(m.I), column) {
						s.requireIfPresent(id, s.sources[child])
					}
				case *schema.DropPrimaryKey:
					if column != nil && s.sameTable(c.T, table) && hasColumn(partColumns(m.P), column) {
						s.requireIfPresent(id, s.sources[child])
					}
				}
			}
		}
	}
}

func (s *Set) references(fk *schema.ForeignKey, table *schema.Table, column *schema.Column) bool {
	if fk.RefTable == nil || !s.sameTable(fk.RefTable, table) {
		return false
	}
	return column == nil || hasColumn(fk.RefColumns, column)
}

func (s *Set) requireIfPresent(id, required string) {
	if _, ok := s.index[required]; ok && id != "" {
		s.Require(id, required)
	}
}

func (s *Set) sameTable(a, b *schema.Table) bool {
	return a != nil && b != nil && s.tableKey(a) == s.tableKey(b)
}

func (s *Set) idOf(action, kind string, name migrate.Name, member string) string {
	id, _ := json.Marshal([]string{action, kind, name.Schema, name.Name, member})
	return string(id)
}

func partColumns(index *schema.Index) []*schema.Column {
	if index == nil {
		return nil
	}
	var columns []*schema.Column
	for _, part := range index.Parts {
		if part.C != nil {
			columns = append(columns, part.C)
		}
	}
	return columns
}

func hasColumn(columns []*schema.Column, column *schema.Column) bool {
	for _, c := range columns {
		if c != nil && c.Name == column.Name {
			return true
		}
	}
	return false
}
