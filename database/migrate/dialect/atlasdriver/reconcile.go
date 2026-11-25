package atlasdriver

import (
	"context"
	"fmt"
	"slices"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// Reconcile spells the desired schema's attributes like the live ones where the
// database considers them equal (the backend decides which attributes, and how
// it asks). Native and the projection are rewritten together; a schema nothing
// changes in is returned as it is, so a second call is a no-op.
func (d *Dialect) Reconcile(ctx context.Context, q engine.Querier, current, desired migrate.Schema) (migrate.Schema, error) {
	cur, err := d.load(current)
	if err != nil {
		return migrate.Schema{}, fmt.Errorf("current: %w", err)
	}
	des, err := d.decode(desired.Native)
	if err != nil {
		return migrate.Schema{}, fmt.Errorf("desired: %w", err)
	}
	keep := map[string]bool{}
	enums := map[string]bool{}
	for _, t := range desired.Tables {
		keep[t.Name.Name] = true
	}
	for _, def := range desired.Definitions {
		enums[def.Name.Name] = def.Kind == enumKind
	}
	changed, err := d.backend.Reconcile(ctx, q, cur, des, func(table string) bool { return keep[table] })
	if err != nil || !changed {
		return desired, err
	}
	native, err := d.encode(des)
	if err != nil {
		return migrate.Schema{}, fmt.Errorf("%s: reconcile desired schema: %w", d.Name(), err)
	}
	projected, err := d.project(des, keep, enums)
	if err != nil {
		return migrate.Schema{}, err
	}
	out := desired
	out.Native, out.Tables = native, projected.Tables
	for i := range out.Tables {
		j := slices.IndexFunc(desired.Tables, func(t migrate.Table) bool { return t.Name == out.Tables[i].Name })
		out.Tables[i].Partial = desired.Tables[j].Partial
	}
	return out, nil
}
