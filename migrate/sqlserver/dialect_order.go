package sqlserver

import (
	"slices"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
	atlas "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// loweredOps are ops with what ordering them across changes needs to know.
type loweredOps struct {
	ops []engine.Op
	// roles lists, per op, the indexes and keys its statements create or drop.
	roles [][]indexRole
	// refs lists, per op, the tables its create statements' foreign keys reference.
	refs [][]migrate.Name
}

func (l loweredOps) concat(other loweredOps) loweredOps {
	return loweredOps{
		ops:   slices.Concat(l.ops, other.ops),
		roles: slices.Concat(l.roles, other.roles),
		refs:  slices.Concat(l.refs, other.refs),
	}
}

// indexRole is one index, primary key or unique constraint an op creates or drops.
type indexRole struct {
	table     migrate.Name
	clustered bool
}

func isClustered(idx *schema.Index) bool {
	return slices.ContainsFunc(idx.Attrs, func(a schema.Attr) bool { _, ok := a.(*atlas.Clustered); return ok })
}

// indexRoleOf reads the index a planned statement creates (in the create
// phase) or drops (in the drop phase). A table's own creation is not one: it
// holds its clustered key and its indexes in order already.
func indexRoleOf(source schema.Change, phase engine.Phase) (indexRole, bool) {
	var idx *schema.Index
	switch c := source.(type) {
	case *schema.AddIndex:
		idx = pick(phase, c.I, nil)
	case *schema.AddPrimaryKey:
		idx = pick(phase, c.P, nil)
	case *schema.DropIndex:
		idx = pick(phase, nil, c.I)
	case *schema.DropPrimaryKey:
		idx = pick(phase, nil, c.P)
	case *schema.ModifyIndex:
		idx = pick(phase, c.To, c.From)
	case *schema.ModifyPrimaryKey:
		idx = pick(phase, c.To, c.From)
	}
	if idx == nil {
		return indexRole{}, false
	}
	return indexRole{table: tableNameOf(idx.Table), clustered: isClustered(idx)}, true
}

func pick(phase engine.Phase, create, drop *schema.Index) *schema.Index {
	switch phase {
	case engine.PhaseCreate:
		return create
	case engine.PhaseDrop:
		return drop
	}
	return nil
}

// linkClusteredFirst orders the indexes of one table around its clustered
// key: creating a clustered index rebuilds every nonclustered index on the
// table, so the clustered one is created first and dropped last.
func linkClusteredFirst(l loweredOps) {
	for c, clusteredRoles := range l.roles {
		for n, nonclusteredRoles := range l.roles {
			if c == n || l.ops[c].Phase != l.ops[n].Phase {
				continue
			}
			if !sharesTable(clusteredRoles, nonclusteredRoles) {
				continue
			}
			switch l.ops[c].Phase {
			case engine.PhaseCreate:
				requires(&l.ops[n], l.ops[c].ID)
			case engine.PhaseDrop:
				requires(&l.ops[c], l.ops[n].ID)
			}
		}
	}
}

// sharesTable reports a clustered role in first and a nonclustered one in
// second on the same table.
func sharesTable(first, second []indexRole) bool {
	return slices.ContainsFunc(first, func(f indexRole) bool {
		return f.clustered && slices.ContainsFunc(second, func(s indexRole) bool { return !s.clustered && s.table == f.table })
	})
}

// linkNewForeignKeys makes every op that creates a foreign key wait for the
// re-creation of a key it references, which the same plan rebuilds.
func linkNewForeignKeys(l loweredOps, rebuilds []engine.Rebuild) {
	for _, key := range rebuilds {
		if !referencable(key.Dependent) {
			continue
		}
		created := "create:" + key.Change.ID
		for i, refs := range l.refs {
			if slices.Contains(refs, key.Dependent.Table) {
				requires(&l.ops[i], created)
			}
		}
	}
}
