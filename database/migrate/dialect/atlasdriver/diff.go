package atlasdriver

import (
	"fmt"
	"slices"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/changeset"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// Diff decodes both snapshots, keeps the tables still in each projection and
// describes the changes between them.
func (d *Dialect) Diff(current, desired migrate.Schema) (engine.Diff, error) {
	cur, err := d.load(current)
	if err != nil {
		return nil, fmt.Errorf("current: %w", err)
	}
	des, err := d.load(desired)
	if err != nil {
		return nil, fmt.Errorf("desired: %w", err)
	}
	if cur.Name != des.Name {
		return nil, fmt.Errorf("%s: current schema %q and desired schema %q differ", d.Name(), cur.Name, des.Name)
	}
	changes, err := d.diffSchemas(cur, des)
	if err != nil {
		return nil, err
	}
	changes = withoutUnmanagedEnums(changes, current, desired)
	set, err := changeset.Describe(changes, d.backend.Changes())
	if err != nil {
		return nil, err
	}
	if err := d.relaxRebuildDrops(changes, set); err != nil {
		return nil, err
	}
	forcing, err := d.backend.Forcing(set, changes)
	if err != nil {
		return nil, fmt.Errorf("%s: forcing changes: %w", d.Name(), err)
	}
	return &planDiff{d: d, changes: changes, set: set, forcing: forcing}, nil
}

// withoutUnmanagedEnums drops the changes of enum types that neither projection
// lists. A table's enum stays in the decoded schema because its columns use it,
// but when the engine narrows a schema to one unit's objects the enum belongs
// to another unit, whose change must not read as drift in this one.
func withoutUnmanagedEnums(changes []schema.Change, current, desired migrate.Schema) []schema.Change {
	managed := map[string]bool{}
	for _, s := range []migrate.Schema{current, desired} {
		for _, def := range s.Definitions {
			if def.Kind == enumKind {
				managed[def.Name.Name] = true
			}
		}
	}
	return slices.DeleteFunc(slices.Clone(changes), func(c schema.Change) bool {
		var object schema.Object
		switch c := c.(type) {
		case *schema.AddObject:
			object = c.O
		case *schema.DropObject:
			object = c.O
		case *schema.ModifyObject:
			object = c.To
		}
		enum, ok := object.(*schema.EnumType)
		return ok && !managed[enum.T]
	})
}

// diffSchemas is Atlas's schema diff plus the check-expression binding: Atlas
// ignores edited check expressions on some backends, so they are compared as
// parsed SQL and planned as one modification of the check. It is one change, not
// a drop and an add, so a declared scope that preserves drops still applies it.
func (d *Dialect) diffSchemas(current, desired *schema.Schema) ([]schema.Change, error) {
	changes, err := d.atlas.Differ.SchemaDiff(current, desired)
	if err != nil {
		return nil, fmt.Errorf("%s: diff schemas: %w", d.Name(), err)
	}
	for _, table := range desired.Tables {
		before, ok := current.Table(table.Name)
		if !ok {
			continue
		}
		checks, err := d.editedChecks(before, table)
		if err != nil {
			return nil, err
		}
		if len(checks) > 0 {
			changes = bindChecks(changes, table, checks)
		}
	}
	return changes, nil
}

func (d *Dialect) editedChecks(before, after *schema.Table) ([]schema.Change, error) {
	var out []schema.Change
	for _, check := range after.Checks() {
		if check.Name == "" {
			continue
		}
		i := slices.IndexFunc(before.Checks(), func(c *schema.Check) bool { return c.Name == check.Name })
		if i < 0 {
			continue
		}
		old := before.Checks()[i]
		same, err := sameCheck(old.Expr, check.Expr, d.Name())
		if err != nil {
			return nil, fmt.Errorf("%s: check %q of table %q: %w", d.Name(), check.Name, after.Name, err)
		}
		if !same {
			out = append(out, &schema.ModifyCheck{From: old, To: check})
		}
	}
	return out, nil
}

// bindChecks adds the check changes to the table's modification, creating it
// from the desired table (what a SQLite rebuild recreates) when the diff has none.
func bindChecks(changes []schema.Change, after *schema.Table, checks []schema.Change) []schema.Change {
	for _, change := range changes {
		if m, ok := change.(*schema.ModifyTable); ok && m.T.Name == after.Name {
			m.Changes = append(m.Changes, checks...)
			return changes
		}
	}
	return append(changes, &schema.ModifyTable{T: after, Changes: checks})
}

// relaxRebuildDrops removes the requirement a rebuilt table's changes have on
// the drops of their siblings. A rebuild builds the table from the desired
// definition plus every member whose drop is not selected (Lower), so a drop
// that is preserved or left out is simply not applied. The requirements that
// come from structure alone (a column drop needs the drop of an index on it)
// stay: describing the diff without the rebuild rule tells them apart.
func (d *Dialect) relaxRebuildDrops(changes []schema.Change, set *changeset.Set) error {
	cd := d.backend.Changes()
	if cd.Rebuilds == nil {
		return nil
	}
	bare := cd
	bare.Rebuilds = nil
	structural, err := changeset.Describe(changes, bare)
	if err != nil {
		return err
	}
	for _, c := range changes {
		m, ok := c.(*schema.ModifyTable)
		if !ok || !slices.ContainsFunc(m.Changes, cd.Rebuilds) {
			continue
		}
		ids, err := set.Lookup(m)
		if err != nil {
			return err
		}
		drops := map[string]bool{}
		for _, id := range ids {
			if ch, _ := set.Change(id); ch.Action == "drop" {
				drops[id] = true
			}
		}
		for i, ch := range set.Changes {
			if !slices.Contains(ids, ch.ID) {
				continue
			}
			base, _ := structural.Change(ch.ID)
			set.Changes[i].Requires = slices.DeleteFunc(slices.Clone(ch.Requires), func(r string) bool {
				return drops[r] && !slices.Contains(base.Requires, r)
			})
		}
	}
	return nil
}

type planDiff struct {
	d       *Dialect
	changes []schema.Change
	set     *changeset.Set
	forcing []engine.Forcing
}

func (p *planDiff) Changes() []migrate.Change { return slices.Clone(p.set.Changes) }
func (p *planDiff) Forcing() []engine.Forcing { return slices.Clone(p.forcing) }
func (p *planDiff) Warnings() []string        { return nil }
