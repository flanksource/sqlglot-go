package engine

import (
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate"
)

func phaseRank(p Phase) int {
	switch p {
	case PhaseDrop:
		return 0
	case PhaseAlter:
		return 1
	default:
		return 2
	}
}

// lowerOrdered lowers the selection and rebuilds to ops, checks them against
// the planned changes, and returns them in a global dependency order.
func lowerOrdered(diff Diff, changes []migrate.Change, rebuilds []Rebuild, selection []string) ([]Op, error) {
	ops, err := diff.Lower(slices.Clone(selection), rebuilds)
	if err != nil {
		return nil, fmt.Errorf("engine: lower changes: %w", err)
	}
	if err := validateOps(ops, changes); err != nil {
		return nil, err
	}
	return orderOps(ops, rebuildChanges(rebuilds))
}

func validateOps(ops []Op, changes []migrate.Change) error {
	planned := map[string]bool{}
	for _, c := range changes {
		planned[c.ID] = true
	}
	byID := map[string]bool{}
	covered := map[string]bool{}
	for _, op := range ops {
		switch {
		case op.ID == "":
			return fmt.Errorf("engine: op for change %q has no ID", op.Change)
		case byID[op.ID]:
			return fmt.Errorf("engine: duplicate op ID %s", op.ID)
		case !planned[op.Change]:
			return fmt.Errorf("engine: op %s belongs to change %q, which is neither selected nor a rebuild", op.ID, op.Change)
		case op.Phase != PhaseDrop && op.Phase != PhaseAlter && op.Phase != PhaseCreate:
			return fmt.Errorf("engine: op %s has phase %q, want drop, alter or create", op.ID, op.Phase)
		}
		byID[op.ID] = true
		covered[op.Change] = true
	}
	for _, op := range ops {
		for _, r := range op.Requires {
			if !byID[r] || r == op.ID {
				return fmt.Errorf("engine: op %s requires %q, which is not another op of the plan", op.ID, r)
			}
		}
	}
	for _, c := range changes {
		if !covered[c.ID] {
			return fmt.Errorf("engine: change %s lowered to no ops", c.ID)
		}
	}
	return nil
}

// orderOps is a stable topological sort. Edges are the ops' Requires, plus,
// for each rebuild, its drop ops before and its create ops after every op of
// the changes that caused it. Among ready ops the earliest phase wins, then
// the order Lower returned.
func orderOps(ops []Op, rebuilds []migrate.Change) ([]Op, error) {
	index := map[string]int{}
	byChange := map[string][]int{}
	for i, op := range ops {
		index[op.ID] = i
		byChange[op.Change] = append(byChange[op.Change], i)
	}
	waits := make([][]int, len(ops)) // waits[i]: ops that must precede ops[i]
	for i, op := range ops {
		for _, r := range op.Requires {
			waits[i] = append(waits[i], index[r])
		}
	}
	for _, r := range rebuilds {
		for _, i := range byChange[r.ID] {
			for _, cause := range r.Requires {
				for _, p := range byChange[cause] {
					switch ops[i].Phase {
					case PhaseDrop:
						waits[p] = append(waits[p], i)
					case PhaseCreate:
						waits[i] = append(waits[i], p)
					}
				}
			}
		}
	}
	return kahn(ops, waits)
}

func kahn(ops []Op, waits [][]int) ([]Op, error) {
	done := make([]bool, len(ops))
	out := make([]Op, 0, len(ops))
	for len(out) < len(ops) {
		next := -1
		for i := range ops {
			if done[i] || slices.ContainsFunc(waits[i], func(w int) bool { return !done[w] }) {
				continue
			}
			if next < 0 || phaseRank(ops[i].Phase) < phaseRank(ops[next].Phase) {
				next = i
			}
		}
		if next < 0 {
			return nil, fmt.Errorf("engine: ops form a dependency cycle: %s", strings.Join(cycleMembers(ops, waits, done), ", "))
		}
		done[next] = true
		out = append(out, ops[next])
	}
	return out, nil
}

// cycleMembers names the unordered ops that are part of, or wait on, a cycle,
// after pruning those nothing else waits on.
func cycleMembers(ops []Op, waits [][]int, done []bool) []string {
	alive := map[int]bool{}
	for i := range ops {
		if !done[i] {
			alive[i] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for i := range alive {
			waitedOn := false
			for j := range alive {
				if slices.Contains(waits[j], i) {
					waitedOn = true
					break
				}
			}
			if !waitedOn {
				delete(alive, i)
				changed = true
			}
		}
	}
	names := make([]string, 0, len(alive))
	for i := range alive {
		names = append(names, ops[i].ID)
	}
	slices.Sort(names)
	return names
}
