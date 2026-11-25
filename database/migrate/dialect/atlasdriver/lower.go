package atlasdriver

import (
	"context"
	"fmt"
	"slices"
	"strings"

	atlas "ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

const planName = "schema-planner"

// Lower plans the selected changes with the driver's offline planner and maps
// each statement to the changes it implements, one op per (change, phase).
func (p *planDiff) Lower(selection []string, rebuilds []engine.Rebuild) ([]engine.Op, error) {
	ops, err := p.lowerSelected(selection)
	if err != nil {
		return nil, err
	}
	rebuilt, err := p.lowerRebuilds(rebuilds)
	if err != nil {
		return nil, err
	}
	return append(ops, rebuilt...), nil
}

// group is the statements one planned change (or one table rebuild that
// implements several changes) contributes in one phase.
type group struct {
	ids   []string
	phase engine.Phase
	stmts []engine.Statement
	order int // position of its first statement in the planner's output
}

func (p *planDiff) lowerSelected(selection []string) ([]engine.Op, error) {
	if len(selection) == 0 {
		return nil, nil
	}
	selected, err := p.set.Select(p.changes, selection)
	if err != nil {
		return nil, err
	}
	if selected, err = p.keepUnselectedDrops(selected); err != nil {
		return nil, err
	}
	objects, rest := splitObjectChanges(selected)
	plan, err := p.d.atlas.Planner.PlanChanges(context.Background(), planName, rest)
	if err != nil {
		return nil, fmt.Errorf("%s: plan schema: %w", p.d.Name(), err)
	}
	groups, err := p.groupStatements(plan.Changes, !plan.Transactional)
	if err != nil {
		return nil, err
	}
	objectGroups, err := p.objectGroups(objects)
	if err != nil {
		return nil, err
	}
	ops, err := p.opsOf(append(objectGroups, groups...))
	if err != nil {
		return nil, err
	}
	return p.withCascadedDrops(ops, selection), nil
}

// splitObjectChanges separates the modifications of schema objects (an enum
// gaining a value) from the rest: Atlas plans them without naming the change
// each statement implements, so they are planned one at a time.
func splitObjectChanges(selected []schema.Change) (objects, rest []schema.Change) {
	for _, c := range selected {
		if _, ok := c.(*schema.ModifyObject); ok {
			objects = append(objects, c)
		} else {
			rest = append(rest, c)
		}
	}
	return objects, rest
}

// objectGroups plans each object modification alone and attributes all its
// statements to it. They sort before everything else the planner emitted, so a
// value is added before a column of that type.
func (p *planDiff) objectGroups(objects []schema.Change) ([]*group, error) {
	groups := make([]*group, 0, len(objects))
	for i, c := range objects {
		plan, err := p.d.atlas.Planner.PlanChanges(context.Background(), planName, []schema.Change{c})
		if err != nil {
			return nil, fmt.Errorf("%s: plan object change: %w", p.d.Name(), err)
		}
		ids, err := p.set.Lookup(c)
		if err != nil {
			return nil, err
		}
		g := &group{ids: ids, phase: engine.PhaseAlter, order: i - len(objects)}
		for _, planned := range plan.Changes {
			g.stmts = append(g.stmts, engine.Statement{SQL: planned.Cmd, NonTransactional: !plan.Transactional})
		}
		groups = append(groups, g)
	}
	return groups, nil
}

// withCascadedDrops gives a selected drop of an index, key or check that goes
// with a dropped column of its table an op of its own with no statements: the
// database removes the member with the column, so the planner emits nothing
// for it, but the change stays accounted for.
func (p *planDiff) withCascadedDrops(ops []engine.Op, selection []string) []engine.Op {
	covered := map[string]bool{}
	columnDrops := map[migrate.Name]bool{}
	for _, op := range ops {
		covered[op.Change] = true
	}
	for _, id := range selection {
		if c, ok := p.set.Change(id); ok && c.Action == "drop" && c.Kind == "column" {
			columnDrops[c.Object] = true
		}
	}
	for _, id := range selection {
		c, ok := p.set.Change(id)
		if !ok || covered[id] || c.Action != "drop" || !columnDrops[c.Object] {
			continue
		}
		switch c.Kind {
		case "index", "primary-key", "foreign-key", "check":
			ops = append(ops, engine.Op{ID: id + "#" + string(engine.PhaseDrop), Change: id, Phase: engine.PhaseDrop, Object: c.Object})
		}
	}
	return ops
}

func (p *planDiff) groupStatements(planned []*atlas.Change, nonTransactional bool) ([]*group, error) {
	rebuilt, err := p.rebuiltTables(planned)
	if err != nil {
		return nil, err
	}
	var groups []*group
	byKey := map[string]*group{}
	var last *group
	for i, c := range planned {
		if c.Cmd == "PRAGMA foreign_keys = off" || c.Cmd == "PRAGMA foreign_keys = on" {
			continue
		}
		if len(c.Args) > 0 {
			return nil, fmt.Errorf("%s: parameterized statement %q is not supported", p.d.Name(), c.Cmd)
		}
		stmt := engine.Statement{SQL: c.Cmd, NonTransactional: nonTransactional}
		source := p.statementSource(c, rebuilt)
		if source == nil {
			if last == nil {
				return nil, fmt.Errorf("%s: statement %q has no source change", p.d.Name(), c.Cmd)
			}
			last.stmts = append(last.stmts, stmt)
			continue
		}
		ids, phase, err := p.classify(source, rebuilt)
		if err != nil {
			return nil, err
		}
		key := string(phase) + "\x00" + strings.Join(ids, "\x00")
		g, ok := byKey[key]
		if !ok {
			g = &group{ids: ids, phase: phase, order: i}
			byKey[key] = g
			groups = append(groups, g)
		}
		g.stmts = append(g.stmts, stmt)
		last = g
	}
	return groups, nil
}

// statementSource is the change a statement implements. Atlas names no change
// for the `DROP INDEX name` its SQLite planner emits for an index dropped from
// an altered table, and its PostgreSQL planner names the whole table
// modification for the `DROP INDEX` of an index that is no constraint; either
// would leave the drop with no op of its own (or an op of a sibling change), so
// the statement resolves to the DropIndex it names.
func (p *planDiff) statementSource(c *atlas.Change, rebuilt map[string][]string) schema.Change {
	switch src := c.Source.(type) {
	case nil:
		return p.dropIndexSource(c.Cmd, p.tableMembers())
	case *schema.ModifyTable:
		if rebuilt[src.T.Name] == nil {
			if drop := p.dropIndexSource(c.Cmd, src.Changes); drop != nil {
				return drop
			}
		}
	}
	return c.Source
}

// tableMembers lists the member changes of every modified table of the diff.
func (p *planDiff) tableMembers() []schema.Change {
	var out []schema.Change
	for _, c := range p.changes {
		if m, ok := c.(*schema.ModifyTable); ok {
			out = append(out, m.Changes...)
		}
	}
	return out
}

// dropIndexSource finds, among candidates, the DropIndex a `DROP INDEX name`
// statement (the name possibly quoted and schema-qualified) implements.
func (p *planDiff) dropIndexSource(cmd string, candidates []schema.Change) schema.Change {
	rest, ok := strings.CutPrefix(cmd, "DROP INDEX ")
	if !ok {
		return nil
	}
	name := strings.NewReplacer("`", "", `"`, "").Replace(rest)
	for _, c := range candidates {
		if drop, ok := c.(*schema.DropIndex); ok && (name == drop.I.Name || strings.HasSuffix(name, "."+drop.I.Name)) {
			return drop
		}
	}
	return nil
}

// rebuiltTables maps each table the planner rebuilds (a table-level change in
// the plan) to the changes the rebuild implements. Only a dialect that rebuilds
// tables has any: elsewhere a planner names the table modification as the
// source of statements that implement only some of its members.
func (p *planDiff) rebuiltTables(planned []*atlas.Change) (map[string][]string, error) {
	out := map[string][]string{}
	if p.d.backend.Changes().Rebuilds == nil {
		return out, nil
	}
	for _, c := range planned {
		if m, ok := c.Source.(*schema.ModifyTable); ok {
			ids, err := p.set.Lookup(m)
			if err != nil {
				return nil, err
			}
			out[m.T.Name] = ids
		}
	}
	return out, nil
}

// classify names the changes a statement implements and its phase. Every
// statement of a rebuilt table (creating the temporary table, copying rows,
// dropping, renaming, re-creating indexes) is one alter group, so the
// statements stay in the planner's order.
func (p *planDiff) classify(source schema.Change, rebuilt map[string][]string) ([]string, engine.Phase, error) {
	ids, err := p.set.Lookup(source)
	if err != nil {
		return nil, "", err
	}
	if table, ok := p.singleTable(ids); ok && rebuilt[table] != nil {
		return rebuilt[table], engine.PhaseAlter, nil
	}
	if len(ids) > 1 {
		return ids, engine.PhaseAlter, nil
	}
	return ids, phaseOf(source), nil
}

func (p *planDiff) singleTable(ids []string) (string, bool) {
	name := ""
	for _, id := range ids {
		c, ok := p.set.Change(id)
		if !ok || (name != "" && c.Object.Name != name) {
			return "", false
		}
		name = c.Object.Name
	}
	return name, name != ""
}

func (p *planDiff) opsOf(groups []*group) ([]engine.Op, error) {
	ops := map[string]*engine.Op{}
	order := map[string]int{}
	var list []*engine.Op
	add := func(id string, phase engine.Phase, at int) (*engine.Op, error) {
		opID := id + "#" + string(phase)
		if op, ok := ops[opID]; ok {
			return op, nil
		}
		c, ok := p.set.Change(id)
		if !ok {
			return nil, fmt.Errorf("%s: planned statement maps to unknown change %s", p.d.Name(), id)
		}
		op := &engine.Op{ID: opID, Change: id, Phase: phase, Object: c.Object}
		ops[opID], order[opID] = op, at
		list = append(list, op)
		return op, nil
	}
	for _, g := range groups {
		first, err := add(g.ids[0], g.phase, g.order)
		if err != nil {
			return nil, err
		}
		first.Statements = append(first.Statements, g.stmts...)
		for _, id := range g.ids[1:] {
			if hasOp(ops, id) {
				continue
			}
			sibling, err := add(id, g.phase, g.order)
			if err != nil {
				return nil, err
			}
			sibling.Requires = []string{first.ID}
		}
	}
	return p.finish(list, order), nil
}

func hasOp(ops map[string]*engine.Op, id string) bool {
	for _, phase := range []engine.Phase{engine.PhaseDrop, engine.PhaseAlter, engine.PhaseCreate} {
		if _, ok := ops[id+"#"+string(phase)]; ok {
			return true
		}
	}
	return false
}

// finish keeps the planner's order where the engine's phase sort would invert
// it: an op the planner placed before an earlier-phase op on the same object
// (or on a type object such as an enum) is required by it.
func (p *planDiff) finish(list []*engine.Op, order map[string]int) []engine.Op {
	slices.SortStableFunc(list, func(a, b *engine.Op) int { return order[a.ID] - order[b.ID] })
	for j, later := range list {
		for _, earlier := range list[:j] {
			if phaseRank(earlier.Phase) > phaseRank(later.Phase) && p.related(earlier, later) && !slices.Contains(later.Requires, earlier.ID) {
				later.Requires = append(later.Requires, earlier.ID)
			}
		}
	}
	out := make([]engine.Op, len(list))
	for i, op := range list {
		out[i] = *op
	}
	return out
}

func (p *planDiff) related(a, b *engine.Op) bool {
	return a.Object == b.Object || p.isType(a.Change) || p.isType(b.Change)
}

func (p *planDiff) isType(id string) bool {
	c, ok := p.set.Change(id)
	return ok && c.Kind == enumKind
}

func phaseRank(p engine.Phase) int {
	switch p {
	case engine.PhaseDrop:
		return 0
	case engine.PhaseAlter:
		return 1
	}
	return 2
}

func phaseOf(c schema.Change) engine.Phase {
	switch c.(type) {
	case *schema.DropTable, *schema.DropColumn, *schema.DropIndex, *schema.DropPrimaryKey, *schema.DropForeignKey, *schema.DropCheck,
		*schema.DropObject, *schema.DropView, *schema.DropTrigger, *schema.DropAttr:
		return engine.PhaseDrop
	case *schema.AddTable, *schema.AddColumn, *schema.AddIndex, *schema.AddPrimaryKey, *schema.AddForeignKey, *schema.AddCheck,
		*schema.AddObject, *schema.AddView, *schema.AddTrigger, *schema.AddAttr:
		return engine.PhaseCreate
	}
	return engine.PhaseAlter
}
