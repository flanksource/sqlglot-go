package atlasdriver

import (
	"fmt"
	"slices"

	"ariga.io/atlas/sql/schema"
)

// keepUnselectedDrops returns the selected changes with each table that will be
// rebuilt rebuilt from the desired table plus every member whose drop change is
// not selected. A rebuild recreates the table from the desired definition, so
// without this a drop that the scope preserves (or the caller left out) would
// still lose its member and its data. Tables that are altered in place need
// nothing: an unselected drop is simply not planned.
func (p *planDiff) keepUnselectedDrops(selected []schema.Change) ([]schema.Change, error) {
	rebuilds := p.d.backend.Changes().Rebuilds
	if rebuilds == nil {
		return selected, nil
	}
	originals := map[string]*schema.ModifyTable{}
	for _, c := range p.changes {
		if m, ok := c.(*schema.ModifyTable); ok {
			originals[m.T.Name] = m
		}
	}
	out := make([]schema.Change, len(selected))
	for i, c := range selected {
		out[i] = c
		m, ok := c.(*schema.ModifyTable)
		if !ok || !slices.ContainsFunc(m.Changes, rebuilds) {
			continue
		}
		var kept []schema.Change
		for _, child := range originals[m.T.Name].Changes {
			if isDrop(child) && !slices.Contains(m.Changes, child) {
				kept = append(kept, child)
			}
		}
		if len(kept) == 0 {
			continue
		}
		merged, err := mergeMembers(m.T, kept)
		if err != nil {
			return nil, fmt.Errorf("%s: keep members of rebuilt table %q: %w", p.d.Name(), m.T.Name, err)
		}
		out[i] = &schema.ModifyTable{T: merged, Changes: m.Changes}
	}
	return out, nil
}

func isDrop(c schema.Change) bool {
	switch c.(type) {
	case *schema.DropColumn, *schema.DropIndex, *schema.DropPrimaryKey, *schema.DropForeignKey, *schema.DropCheck:
		return true
	}
	return false
}

// mergeMembers copies the desired table and adds the members the drop changes
// name. The desired table itself is left alone.
func mergeMembers(desired *schema.Table, drops []schema.Change) (*schema.Table, error) {
	t := *desired
	t.Columns, t.Indexes = slices.Clone(desired.Columns), slices.Clone(desired.Indexes)
	t.ForeignKeys, t.Attrs = slices.Clone(desired.ForeignKeys), slices.Clone(desired.Attrs)
	for _, c := range drops {
		if d, ok := c.(*schema.DropColumn); ok {
			kept := *d.C
			kept.Indexes, kept.ForeignKeys = nil, nil
			t.AddColumns(&kept)
		}
	}
	for _, c := range drops {
		switch d := c.(type) {
		case *schema.DropPrimaryKey:
			t.SetPrimaryKey(rebindIndex(d.P, &t))
		case *schema.DropIndex:
			t.AddIndexes(rebindIndex(d.I, &t))
		case *schema.DropForeignKey:
			t.AddForeignKeys(rebindForeignKey(d.F, &t))
		case *schema.DropCheck:
			t.AddChecks(d.C)
		}
	}
	return &t, checkBound(&t)
}

// checkBound fails when a kept index or foreign key names a column the merged
// table does not have, which means its column is being dropped while it stays.
func checkBound(t *schema.Table) error {
	for _, i := range t.Indexes {
		for _, p := range i.Parts {
			if p.C == nil && p.X == nil {
				return fmt.Errorf("index %q names a column the table no longer has", i.Name)
			}
		}
	}
	for _, fk := range t.ForeignKeys {
		if slices.Contains(fk.Columns, nil) {
			return fmt.Errorf("foreign key %q names a column the table no longer has", fk.Symbol)
		}
	}
	return nil
}

func rebindIndex(i *schema.Index, table *schema.Table) *schema.Index {
	out := &schema.Index{Name: i.Name, Unique: i.Unique, Table: table, Attrs: i.Attrs}
	for _, p := range i.Parts {
		part := &schema.IndexPart{SeqNo: p.SeqNo, Desc: p.Desc, X: p.X, Attrs: p.Attrs}
		if p.C != nil {
			part.C, _ = table.Column(p.C.Name)
		}
		out.Parts = append(out.Parts, part)
	}
	return out
}

func rebindForeignKey(fk *schema.ForeignKey, table *schema.Table) *schema.ForeignKey {
	out := &schema.ForeignKey{Symbol: fk.Symbol, Table: table, RefTable: fk.RefTable, RefColumns: fk.RefColumns, OnUpdate: fk.OnUpdate, OnDelete: fk.OnDelete}
	for _, c := range fk.Columns {
		col, _ := table.Column(c.Name)
		out.Columns = append(out.Columns, col)
	}
	return out
}
