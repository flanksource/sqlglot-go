package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate"
)

type components struct{ parent []int }

func newComponents(n int) *components {
	c := &components{parent: make([]int, n)}
	for i := range c.parent {
		c.parent[i] = i
	}
	return c
}

func (c *components) find(i int) int {
	for c.parent[i] != i {
		c.parent[i] = c.parent[c.parent[i]]
		i = c.parent[i]
	}
	return i
}

func (c *components) union(a, b int) {
	if ra, rb := c.find(a), c.find(b); ra != rb {
		c.parent[max(ra, rb)] = min(ra, rb)
	}
}

// buildUnits groups the ordered ops into connected components: ops linked by
// Requires, by sharing a change, by a rebuild and its cause, or by object.
func buildUnits(ops []Op, changes []migrate.Change, pre map[string][]Precondition, transactionalDDL, atomic bool) []Unit {
	comp := newComponents(len(ops))
	first := map[string]int{}
	firstOnObject := map[migrate.Name]int{}
	opIndex := map[string]int{}
	for i, op := range ops {
		opIndex[op.ID] = i
	}
	for i, op := range ops {
		for _, r := range op.Requires {
			comp.union(i, opIndex[r])
		}
		linkFirst(comp, first, op.Change, i)
		if op.Object != (migrate.Name{}) {
			linkFirst(comp, firstOnObject, op.Object, i)
		}
		if atomic {
			comp.union(i, 0)
		}
	}
	for _, c := range changes {
		for _, cause := range c.Requires {
			if c.Action == actionRebuild {
				comp.union(first[c.ID], first[cause])
			}
		}
	}
	return collectUnits(ops, comp, changes, pre, transactionalDDL)
}

func linkFirst[K comparable](comp *components, first map[K]int, key K, i int) {
	if j, ok := first[key]; ok {
		comp.union(i, j)
		return
	}
	first[key] = i
}

func collectUnits(ops []Op, comp *components, changes []migrate.Change, pre map[string][]Precondition, transactionalDDL bool) []Unit {
	byID := map[string]migrate.Change{}
	for _, c := range changes {
		byID[c.ID] = c
	}
	units := []Unit{}
	slot := map[int]int{}
	for i, op := range ops {
		root := comp.find(i)
		s, ok := slot[root]
		if !ok {
			s = len(units)
			slot[root] = s
			units = append(units, Unit{})
		}
		units[s].Ops = append(units[s].Ops, op)
	}
	for i := range units {
		finishUnit(&units[i], byID, pre, transactionalDDL)
	}
	return units
}

func finishUnit(u *Unit, byID map[string]migrate.Change, pre map[string][]Precondition, transactionalDDL bool) {
	opIDs := make([]string, len(u.Ops))
	u.Transactional = transactionalDDL
	for i, op := range u.Ops {
		opIDs[i] = op.ID
		u.Changes = append(u.Changes, op.Change)
		u.Objects = append(u.Objects, op.Object)
		for _, s := range op.Statements {
			if s.NonTransactional {
				u.Transactional = false
			}
		}
	}
	u.Changes = sortedUnique(u.Changes)
	for _, id := range u.Changes {
		u.Objects = append(u.Objects, byID[id].Object)
		u.Preconditions = append(u.Preconditions, pre[id]...)
	}
	u.Objects = slices.DeleteFunc(u.Objects, func(n migrate.Name) bool { return n == migrate.Name{} })
	slices.SortFunc(u.Objects, compareName)
	u.Objects = slices.Compact(u.Objects)
	slices.SortFunc(u.Preconditions, func(a, b Precondition) int { return strings.Compare(a.ID, b.ID) })
	slices.Sort(opIDs)
	sum := sha256.Sum256([]byte(strings.Join(opIDs, "\x00")))
	u.ID = hex.EncodeToString(sum[:])[:12]
}
