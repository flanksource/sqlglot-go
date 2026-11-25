package sqlserver

import (
	"fmt"
	"slices"

	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// lowerRebuilds gives each rebuild a drop op and a create op on the
// dependent's table, rendered from its live definition, and orders the ops of
// keys and the foreign keys that reference them.
func (p *planDiff) lowerRebuilds(planned []engine.Op, rebuilds []engine.Rebuild) (loweredOps, error) {
	var l loweredOps
	for _, r := range rebuilds {
		if err := p.checkDefaultRebuild(planned, r); err != nil {
			return loweredOps{}, err
		}
		drop, create, err := rebuildOps(r)
		if err != nil {
			return loweredOps{}, err
		}
		var roles []indexRole
		if r.Dependent.Index != nil {
			roles = []indexRole{{table: r.Dependent.Table, clustered: r.Dependent.Index.Clustered}}
		}
		l.ops = append(l.ops, drop, create)
		l.roles = append(l.roles, roles, roles)
		l.refs = append(l.refs, nil, nil)
	}
	linkKeysAndReferences(l.ops, rebuilds)
	return l, nil
}

func rebuildOps(r engine.Rebuild) (drop, create engine.Op, err error) {
	dep := r.Dependent
	dropSQL, err := dropStatements(dep)
	if err != nil {
		return drop, create, err
	}
	createSQL, err := createStatements(dep)
	if err != nil {
		return drop, create, err
	}
	drop = engine.Op{ID: "drop:" + r.Change.ID, Change: r.Change.ID, Phase: engine.PhaseDrop, Object: dep.Table, Statements: statements(dropSQL)}
	create = engine.Op{ID: "create:" + r.Change.ID, Change: r.Change.ID, Phase: engine.PhaseCreate, Object: dep.Table, Statements: statements(createSQL), Requires: []string{drop.ID}}
	return drop, create, nil
}

func statements(sql []string) []engine.Statement {
	out := make([]engine.Statement, len(sql))
	for i, s := range sql {
		out[i] = engine.Statement{SQL: s, SizeOfData: readsEveryRow(s)}
	}
	return out
}

// checkDefaultRebuild refuses a default rebuilt beside a change of its own
// column: that change already drops and re-binds the default, so rebuilding it
// too would drop the constraint twice.
func (p *planDiff) checkDefaultRebuild(planned []engine.Op, r engine.Rebuild) error {
	dep := r.Dependent
	if dep.Kind != engine.DependentDefault || len(dep.On) != 1 {
		return nil
	}
	for _, op := range planned {
		c, ok := p.set.Change(op.Change)
		if ok && c.Kind == "column" && c.Action == "modify" && c.Object == dep.Table && c.Member == dep.On[0].Column {
			return fmt.Errorf("sqlserver: default %s of %s.%s is re-bound by change %s itself; Dependents must not list it",
				dep.Name, qualified(dep.Table), dep.On[0].Column, c.ID)
		}
	}
	return nil
}

// linkKeysAndReferences makes a foreign key's drop come before the drop of the
// key it references, and its create after that key's create.
func linkKeysAndReferences(ops []engine.Op, rebuilds []engine.Rebuild) {
	index := map[string]int{}
	for i, op := range ops {
		index[op.ID] = i
	}
	for _, key := range rebuilds {
		if !referencable(key.Dependent) {
			continue
		}
		for _, fk := range rebuilds {
			if fk.Dependent.Kind != engine.DependentForeignKey || fk.Dependent.ForeignKey.References != key.Dependent.Table {
				continue
			}
			requires(&ops[index["drop:"+key.Change.ID]], "drop:"+fk.Change.ID)
			requires(&ops[index["create:"+fk.Change.ID]], "create:"+key.Change.ID)
		}
	}
}

// referencable reports a key a foreign key can reference.
func referencable(dep engine.Dependent) bool {
	switch dep.Kind {
	case engine.DependentPrimaryKey:
		return true
	case engine.DependentIndex:
		return dep.Index != nil && (dep.Index.Unique || dep.Index.Constraint)
	}
	return false
}

func requires(op *engine.Op, id string) {
	if !slices.Contains(op.Requires, id) {
		op.Requires = append(op.Requires, id)
	}
}
