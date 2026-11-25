package sqlserver

import (
	"fmt"
	"math"
	"slices"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
	atlas "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// Forcing lists the column modifications SQL Server refuses while a dependent
// exists. The rules come from ALTER TABLE ... ALTER COLUMN:
//   - an index or user statistic on the column blocks any change unless the
//     column is varchar, nvarchar or varbinary, the type is unchanged, the new
//     size is equal or larger and the collation is unchanged (a primary key
//     never qualifies);
//   - a primary key or foreign key reference always blocks;
//   - a CHECK or UNIQUE constraint blocks unless only the length of a
//     variable-length column changes;
//   - a default blocks unless only length, precision or scale changes; the
//     column's own change re-binds it, so it forces nothing here.
//
// A foreign key may be defined on another table, and user statistics are not
// part of the schema, so a column in any key a foreign key could reference, and
// every change outside the first rule, forces. Forcing too much only rebuilds a
// dependent that did not strictly need it.
func (p *planDiff) Forcing() []engine.Forcing { return slices.Clone(p.forcing) }

func (p *planDiff) findForcing() ([]engine.Forcing, error) {
	var out []engine.Forcing
	for _, c := range p.diff {
		modify, ok := c.(*schema.ModifyTable)
		if !ok {
			continue
		}
		for _, child := range modify.Changes {
			change, ok := child.(*schema.ModifyColumn)
			if !ok {
				continue
			}
			f, forced, err := p.forcingOf(modify.T, change)
			if err != nil {
				return nil, err
			}
			if forced {
				out = append(out, f)
			}
		}
	}
	return out, nil
}

func (p *planDiff) forcingOf(t *schema.Table, change *schema.ModifyColumn) (engine.Forcing, bool, error) {
	mod := modificationOf(change)
	if !mod.changed() {
		return engine.Forcing{}, false, nil
	}
	ids, err := p.set.Lookup(change)
	if err != nil {
		return engine.Forcing{}, false, fmt.Errorf("sqlserver: column change %s.%s: %w", t.Name, change.To.Name, err)
	}
	if len(ids) != 1 {
		return engine.Forcing{}, false, fmt.Errorf("sqlserver: column change %s.%s names %d changes", t.Name, change.To.Name, len(ids))
	}
	ref := engine.ColumnRef{Table: tableNameOf(t), Column: change.To.Name}
	use, err := p.usageOf(ref)
	if err != nil {
		return engine.Forcing{}, false, err
	}
	if !mod.forces(use) {
		return engine.Forcing{}, false, nil
	}
	return engine.Forcing{Change: ids[0], Columns: []engine.ColumnRef{ref}, Retype: mod.retype}, true, nil
}

// modification is what an ALTER COLUMN changes about a column.
type modification struct {
	retype, nullability, collation bool
	// sameVariable is set when both types are the same varchar, nvarchar or
	// varbinary; widening additionally says the new size is no smaller.
	sameVariable, widening bool
}

func modificationOf(c *schema.ModifyColumn) modification {
	m := modification{
		retype:      c.Change.Is(schema.ChangeType),
		nullability: c.Change.Is(schema.ChangeNull),
		collation:   atlas.CollationChanged(c.From, c.To),
	}
	from, fromOK := variableSize(c.From.Type.Type)
	to, toOK := variableSize(c.To.Type.Type)
	if fromOK && toOK && from.name == to.name {
		m.sameVariable = true
		m.widening = to.size >= from.size
	}
	return m
}

func (m modification) changed() bool { return m.retype || m.nullability || m.collation }

// forces applies the rules above to the column's current use.
func (m modification) forces(u columnUse) bool {
	switch {
	case !m.widening || m.collation:
		return true
	case u.key || u.foreignKey:
		return true
	}
	return u.check && (!m.sameVariable || m.nullability)
}

type variable struct {
	name string
	size int
}

// variableSize reads the variable-length types; (max) sorts above every size.
func variableSize(t schema.Type) (variable, bool) {
	switch t := t.(type) {
	case *schema.StringType:
		if t.T == "varchar" || t.T == "nvarchar" {
			return variable{t.T, sizeOrMax(t.Size)}, true
		}
	case *schema.BinaryType:
		if t.T == "varbinary" && t.Size != nil {
			return variable{t.T, sizeOrMax(*t.Size)}, true
		}
	}
	return variable{}, false
}

func sizeOrMax(size int) int {
	if size < 0 {
		return math.MaxInt
	}
	return size
}

// columnUse is how the current schema uses a column.
type columnUse struct {
	// key: part of a primary key or any unique key, which a foreign key may reference.
	key bool
	// foreignKey: on either side of a foreign key.
	foreignKey bool
	check      bool
}

func (p *planDiff) usageOf(c engine.ColumnRef) (columnUse, error) {
	var u columnUse
	table, ok := findTable(p.current, c.Table)
	if !ok {
		return u, fmt.Errorf("sqlserver: column %s.%s changes but its table is not in the current schema", qualified(c.Table), c.Column)
	}
	for _, idx := range table.Indexes {
		if (idx.Primary || idx.Unique || idx.Constraint) && usesKeyColumn(idx, c.Column) {
			u.key = true
		}
	}
	for _, t := range p.current.Tables {
		for _, fk := range t.ForeignKeys {
			u.foreignKey = u.foreignKey || foreignKeyUses(t.Name, fk, c)
		}
	}
	for _, check := range table.Checks {
		uses, err := mentions(check.Expression, c.Column)
		if err != nil {
			return u, fmt.Errorf("sqlserver: check %s on %s: %w", check.Name, qualified(table.Name), err)
		}
		u.check = u.check || uses
	}
	return u, nil
}

func usesKeyColumn(idx migrate.Index, column string) bool {
	for _, c := range idx.Columns {
		if c.Name == column {
			return true
		}
	}
	return false
}
