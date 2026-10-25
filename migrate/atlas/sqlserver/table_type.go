package sqlserver

import (
	"fmt"
	"slices"
	"strings"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"

	"github.com/flanksource/sqlglot-go/migrate/internal/sqlx"
)

// TableType is a CREATE TYPE ... AS TABLE schema object. Its defaults, keys
// and checks are unnamed: SQL Server names a table type's constraints itself.
type TableType struct {
	schema.Object
	Schema  *schema.Schema
	Name    string
	Columns []*schema.Column
	Keys    []*schema.Index
	Checks  []string
}

// TableTypeRef is a routine parameter typed by a user-defined table type.
type TableTypeRef struct {
	schema.Type
	T *TableType
}

func tableTypesOf(s *schema.Schema) []*TableType {
	var types []*TableType
	for _, o := range s.Objects {
		if t, ok := o.(*TableType); ok {
			types = append(types, t)
		}
	}
	return types
}

// FindTableType resolves a [schema.]name parameter type to a table type
// declared in s, matching names case-insensitively as the catalog does.
func FindTableType(s *schema.Schema, qualified string) *TableType {
	parts := strings.Split(qualified, ".")
	for i := range parts {
		parts[i] = strings.Trim(parts[i], "[]")
	}
	if len(parts) > 2 || len(parts) == 2 && !strings.EqualFold(parts[0], s.Name) {
		return nil
	}
	name := parts[len(parts)-1]
	types := tableTypesOf(s)
	i := slices.IndexFunc(types, func(t *TableType) bool { return strings.EqualFold(t.Name, name) })
	if i < 0 {
		return nil
	}
	return types[i]
}

func (s *state) tableTypeDDL(t *TableType) (string, error) {
	b := s.build("CREATE TYPE").Table(&schema.Table{Name: t.Name, Schema: t.Schema}).P("AS TABLE")
	var errs []string
	b.WrapIndent(func(b *sqlx.Builder) {
		b.MapIndent(t.Columns, func(i int, b *sqlx.Builder) {
			if err := tableTypeColumn(b, t.Columns[i]); err != nil {
				errs = append(errs, err.Error())
			}
		})
		for _, key := range t.Keys {
			b.Comma().NL()
			if _, primary := attrOf[*TableTypePrimaryKey](key.Attrs); primary {
				b.P("PRIMARY KEY", clustering(key))
			} else {
				b.P("UNIQUE", clustering(key))
			}
			indexParts(b, key)
		}
		for _, check := range t.Checks {
			b.Comma().NL().P("CHECK")
			b.Wrap(func(b *sqlx.Builder) { b.Text(check) })
		}
	})
	if len(errs) > 0 {
		return "", fmt.Errorf("sqlserver: table type %q: %s", t.Name, strings.Join(errs, ", "))
	}
	return b.String(), nil
}

// TableTypePrimaryKey marks the key of a table type that is its PRIMARY KEY;
// the others are UNIQUE.
type TableTypePrimaryKey struct {
	schema.Attr
}

func tableTypeColumn(b *sqlx.Builder, c *schema.Column) error {
	typ, err := FormatType(c.Type.Type)
	if err != nil {
		return fmt.Errorf("column %q: %w", c.Name, err)
	}
	b.Ident(c.Name).P(typ)
	if collation, ok := attrOf[*schema.Collation](c.Attrs); ok {
		if err := writeCollation(b, collation.V); err != nil {
			return err
		}
	}
	if identity, ok := attrOf[*Identity](c.Attrs); ok {
		b.P(fmt.Sprintf("IDENTITY(%d,%d)", identity.Seed, identity.Increment))
	}
	b.P(nullability(c))
	if c.Default != nil {
		value, err := DefaultSQL(c)
		if err != nil {
			return err
		}
		b.P("DEFAULT", value)
	}
	return nil
}

// tableTypeDiff adds and drops table types, comparing them by their CREATE
// TYPE text. SQL Server cannot alter a table type -- it must be dropped with
// every module that uses it -- so a changed definition is refused rather than
// planned as a cascade.
func tableTypeDiff(from, to *schema.Schema) ([]schema.Change, error) {
	current, desired := tableTypesOf(from), tableTypesOf(to)
	render := &state{}
	var changes []schema.Change
	for _, t := range desired {
		i := slices.IndexFunc(current, func(live *TableType) bool { return strings.EqualFold(live.Name, t.Name) })
		if i < 0 {
			changes = append(changes, &schema.AddObject{O: t})
			continue
		}
		before, err := render.tableTypeDDL(current[i])
		if err != nil {
			return nil, err
		}
		after, err := render.tableTypeDDL(t)
		if err != nil {
			return nil, err
		}
		if before != after {
			return nil, fmt.Errorf("sqlserver: table type %s.%s: changing a table type is %w", to.Name, t.Name, errUnsupported)
		}
	}
	for _, t := range current {
		if !slices.ContainsFunc(desired, func(d *TableType) bool { return strings.EqualFold(d.Name, t.Name) }) {
			changes = append(changes, &schema.DropObject{O: t})
		}
	}
	return changes, nil
}

func (s *state) tableTypeChange(change schema.Change) (bool, error) {
	switch c := change.(type) {
	case *schema.AddObject:
		t, ok := c.O.(*TableType)
		if !ok {
			return false, nil
		}
		ddl, err := s.tableTypeDDL(t)
		if err != nil {
			return true, err
		}
		s.Changes = append(s.Changes, &migrate.Change{Cmd: ddl, Source: c, Comment: fmt.Sprintf("create table type %q", t.Name)})
	case *schema.DropObject:
		t, ok := c.O.(*TableType)
		if !ok {
			return false, nil
		}
		s.append(c, fmt.Sprintf("drop table type %q", t.Name), s.build("DROP TYPE").Table(&schema.Table{Name: t.Name, Schema: t.Schema}))
	default:
		return false, nil
	}
	return true, nil
}
