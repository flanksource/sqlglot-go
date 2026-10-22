package sqlserver

import (
	"fmt"
	"slices"
	"strings"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
	atlas "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
)

func realm(input migrate.Schema, namespaces []string) (*schema.Realm, error) {
	if err := uniqueObjects(input); err != nil {
		return nil, err
	}
	input.Tables = slices.Clone(input.Tables)
	slices.SortFunc(input.Tables, func(a, b migrate.Table) int { return compareNames(a.Name, b.Name) })
	input.Definitions = slices.Clone(input.Definitions)
	slices.SortStableFunc(input.Definitions, func(a, b migrate.Definition) int {
		if a.Kind != b.Kind {
			if a.Kind == "TRIGGER" {
				return 1
			}
			if b.Kind == "TRIGGER" {
				return -1
			}
		}
		return compareNames(a.Name, b.Name)
	})
	r := schema.NewRealm()
	for _, name := range namespaces {
		r.AddSchemas(schema.New(name))
	}
	for _, table := range input.Tables {
		s, ok := r.Schema(table.Name.Schema)
		if !ok || table.Name.Name == "" {
			return nil, fmt.Errorf("sqlserver: table requires a schema and name: %+v", table.Name)
		}
		if _, ok := s.Table(table.Name.Name); ok {
			return nil, fmt.Errorf("sqlserver: duplicate table %s.%s", s.Name, table.Name.Name)
		}
		t, err := convertTable(table)
		if err != nil {
			return nil, fmt.Errorf("sqlserver: table %s.%s: %w", s.Name, table.Name.Name, err)
		}
		s.AddTables(t)
	}
	for _, table := range input.Tables {
		s, _ := r.Schema(table.Name.Schema)
		t, _ := s.Table(table.Name.Name)
		for _, fk := range table.ForeignKeys {
			if err := foreignKey(r, t, fk); err != nil {
				return nil, fmt.Errorf("sqlserver: table %s.%s: %w", s.Name, t.Name, err)
			}
		}
	}
	for _, d := range input.Definitions {
		if err := definition(r, d); err != nil {
			return nil, err
		}
	}
	if err := dependencies(r, input.Definitions); err != nil {
		return nil, err
	}
	return r, nil
}

func uniqueObjects(input migrate.Schema) error {
	seen := make(map[migrate.Name]bool)
	add := func(name migrate.Name) error {
		if seen[name] {
			return fmt.Errorf("sqlserver: duplicate object %+v", name)
		}
		seen[name] = true
		return nil
	}
	for _, table := range input.Tables {
		if err := add(table.Name); err != nil {
			return err
		}
		for _, name := range constraintNames(table) {
			if err := add(migrate.Name{Schema: table.Name.Schema, Name: name}); err != nil {
				return err
			}
		}
	}
	for _, d := range input.Definitions {
		if err := add(d.Name); err != nil {
			return err
		}
	}
	return nil
}

func constraintNames(table migrate.Table) []string {
	var names []string
	for _, c := range table.Columns {
		if c.Default != nil && c.Default.Name != "" {
			names = append(names, c.Default.Name)
		}
	}
	for _, i := range table.Indexes {
		if i.Primary || i.Constraint {
			names = append(names, i.Name)
		}
	}
	for _, f := range table.ForeignKeys {
		names = append(names, f.Name)
	}
	for _, c := range table.Checks {
		names = append(names, c.Name)
	}
	return names
}

func compareNames(a, b migrate.Name) int {
	if a.Schema != b.Schema {
		return strings.Compare(a.Schema, b.Schema)
	}
	return strings.Compare(a.Name, b.Name)
}

func convertTable(input migrate.Table) (*schema.Table, error) {
	if len(input.Columns) == 0 {
		return nil, fmt.Errorf("table has no columns")
	}
	t := schema.NewTable(input.Name.Name)
	for _, c := range input.Columns {
		if _, ok := t.Column(c.Name); ok || c.Name == "" {
			return nil, fmt.Errorf("duplicate or empty column %q", c.Name)
		}
		typ, err := atlas.ParseType(c.Type)
		if err != nil {
			return nil, fmt.Errorf("column %s: %w", c.Name, err)
		}
		col := &schema.Column{Name: c.Name, Type: &schema.ColumnType{Type: typ, Raw: c.Type, Null: c.Nullable}}
		if c.Identity != nil {
			if c.Identity.Increment == 0 {
				return nil, fmt.Errorf("column %s: identity increment must be nonzero", c.Name)
			}
			col.Attrs = append(col.Attrs, &atlas.Identity{Seed: c.Identity.Seed, Increment: c.Identity.Increment})
		}
		if c.Default != nil {
			if strings.TrimSpace(c.Default.Expression) == "" {
				return nil, fmt.Errorf("column %s: empty default expression", c.Name)
			}
			col.Default = &schema.RawExpr{X: c.Default.Expression}
			if c.Default.Name != "" {
				col.Attrs = append(col.Attrs, &atlas.DefaultConstraint{Name: c.Default.Name})
			}
		}
		if c.Collation != "" {
			col.SetCollation(c.Collation)
		}
		t.AddColumns(col)
	}
	for _, idx := range input.Indexes {
		if err := index(t, idx); err != nil {
			return nil, err
		}
	}
	for _, c := range input.Checks {
		if c.Name == "" || strings.TrimSpace(c.Expression) == "" {
			return nil, fmt.Errorf("check requires a name and expression")
		}
		if slices.ContainsFunc(t.Checks(), func(check *schema.Check) bool { return check.Name == c.Name }) {
			return nil, fmt.Errorf("duplicate check %q", c.Name)
		}
		t.AddChecks(&schema.Check{Name: c.Name, Expr: c.Expression})
	}
	return t, nil
}

func index(t *schema.Table, input migrate.Index) error {
	if _, ok := t.Index(input.Name); ok || input.Name == "" || len(input.Columns) == 0 {
		return fmt.Errorf("index requires a unique name and columns: %q", input.Name)
	}
	i := schema.NewIndex(input.Name).SetUnique(input.Unique || input.Primary).SetTable(t)
	for _, part := range input.Columns {
		c, ok := t.Column(part.Name)
		if !ok {
			return fmt.Errorf("index %s: missing column %s", input.Name, part.Name)
		}
		i.AddParts(schema.NewColumnPart(c).SetDesc(part.Descending))
	}
	for _, name := range input.Included {
		if _, ok := t.Column(name); !ok {
			return fmt.Errorf("index %s: missing included column %s", input.Name, name)
		}
	}
	if input.Clustered {
		i.AddAttrs(&atlas.Clustered{})
	}
	if input.Constraint {
		if !i.Unique || input.Filter != "" || len(input.Included) != 0 {
			return fmt.Errorf("index %s: invalid unique constraint", input.Name)
		}
		i.AddAttrs(&atlas.UniqueConstraint{})
	}
	if len(input.Included) > 0 {
		i.AddAttrs(&atlas.IndexInclude{Columns: input.Included})
	}
	if input.Filter != "" {
		i.AddAttrs(&atlas.IndexPredicate{P: input.Filter})
	}
	if input.Primary {
		if t.PrimaryKey != nil || input.Filter != "" || len(input.Included) != 0 {
			return fmt.Errorf("invalid or duplicate primary key %s", input.Name)
		}
		t.SetPrimaryKey(i)
	} else {
		t.AddIndexes(i)
	}
	return nil
}

func foreignKey(r *schema.Realm, t *schema.Table, input migrate.ForeignKey) error {
	if _, ok := t.ForeignKey(input.Name); ok || input.Name == "" || len(input.Columns) == 0 || len(input.Columns) != len(input.ReferencedColumns) {
		return fmt.Errorf("foreign key %q requires a unique name and matching columns", input.Name)
	}
	s, ok := r.Schema(input.References.Schema)
	if !ok {
		return fmt.Errorf("foreign key %s: missing referenced schema %s", input.Name, input.References.Schema)
	}
	ref, ok := s.Table(input.References.Name)
	if !ok {
		return fmt.Errorf("foreign key %s: missing referenced table %s", input.Name, input.References.Name)
	}
	f := schema.NewForeignKey(input.Name).SetTable(t).SetRefTable(ref)
	for i, name := range input.Columns {
		c, ok := t.Column(name)
		if !ok {
			return fmt.Errorf("foreign key %s: missing column %s", input.Name, name)
		}
		rc, ok := ref.Column(input.ReferencedColumns[i])
		if !ok {
			return fmt.Errorf("foreign key %s: missing referenced column %s", input.Name, input.ReferencedColumns[i])
		}
		f.AddColumns(c).AddRefColumns(rc)
	}
	var err error
	if f.OnDelete, err = referenceAction(input.OnDelete); err != nil {
		return err
	}
	if f.OnUpdate, err = referenceAction(input.OnUpdate); err != nil {
		return err
	}
	t.AddForeignKeys(f)
	return nil
}

func referenceAction(value string) (schema.ReferenceOption, error) {
	switch strings.ToUpper(value) {
	case "", "NO ACTION":
		return schema.NoAction, nil
	case "CASCADE":
		return schema.Cascade, nil
	case "SET NULL":
		return schema.SetNull, nil
	case "SET DEFAULT":
		return schema.SetDefault, nil
	default:
		return "", fmt.Errorf("sqlserver: invalid reference action %q", value)
	}
}

func definition(r *schema.Realm, input migrate.Definition) error {
	s, ok := r.Schema(input.Name.Schema)
	if !ok {
		return fmt.Errorf("sqlserver: definition requires a schema: %+v", input.Name)
	}
	if input.Kind == "VIEW" || input.Kind == "TRIGGER" {
		return module(r, s, input)
	}
	if input.Parent != nil || input.Disabled {
		return fmt.Errorf("sqlserver: parent and disabled state only apply to triggers")
	}
	routine, err := atlas.ParseRoutine(input.SQL)
	if err != nil {
		return err
	}
	if routine.Name != input.Name.Name || !strings.EqualFold(routine.Kind, input.Kind) {
		return fmt.Errorf("sqlserver: definition name or kind does not match %+v (%s)", input.Name, input.Kind)
	}
	if routine.Schema != input.Name.Schema {
		return fmt.Errorf("sqlserver: routine must name its schema %s", input.Name.Schema)
	}
	if _, ok := s.Proc(input.Name.Name); ok {
		return fmt.Errorf("sqlserver: duplicate definition %+v", input.Name)
	}
	if _, ok := s.Func(input.Name.Name); ok {
		return fmt.Errorf("sqlserver: duplicate definition %+v", input.Name)
	}
	return routine.AddTo(s)
}
