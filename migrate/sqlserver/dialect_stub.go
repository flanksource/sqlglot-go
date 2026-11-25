package sqlserver

import (
	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
)

// stubTables holds the placeholder tables a conversion creates for foreign-key
// targets outside the schema.
type stubTables struct {
	byName map[migrate.Name]*schema.Table
}

func (s *stubTables) holds(t *schema.Table) bool {
	if t.Schema == nil {
		return false
	}
	return s.byName[migrate.Name{Schema: t.Schema.Name, Name: t.Name}] == t
}

// stub returns the placeholder for name, owned by a schema that is not part of the realm.
func (s *stubTables) stub(r *schema.Realm, name migrate.Name) *schema.Table {
	if t, ok := s.byName[name]; ok {
		return t
	}
	owner, ok := r.Schema(name.Schema)
	if !ok {
		owner = schema.New(name.Schema)
	}
	t := schema.NewTable(name.Name).SetSchema(owner)
	s.byName[name] = t
	return t
}

// addColumn adds a referenced column to a placeholder. Only its name is used:
// the diff compares a foreign key's target by table and column names.
func (s *stubTables) addColumn(t *schema.Table, name string) *schema.Column {
	c := schema.NewColumn(name).SetType(&schema.IntegerType{T: "int"})
	t.AddColumns(c)
	return c
}

// referencedTable finds the table a foreign key references, or a placeholder
// for one the schema does not hold.
func referencedTable(r *schema.Realm, fk migrate.ForeignKey, stubs *stubTables) *schema.Table {
	if s, ok := r.Schema(fk.References.Schema); ok {
		if ref, ok := s.Table(fk.References.Name); ok {
			return ref
		}
	}
	return stubs.stub(r, fk.References)
}
