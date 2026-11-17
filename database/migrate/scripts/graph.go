package scripts

import (
	"fmt"
	"slices"
	"strings"
)

func Ordered(items []Script) ([]Script, error) {
	byID := map[string]Script{}
	for _, s := range items {
		if s.ID == "" || strings.TrimSpace(s.SQL) == "" {
			return nil, fmt.Errorf("script requires an ID and nonempty SQL: %q", s.Path)
		}
		if s.Phase != Pre && s.Phase != Post {
			return nil, fmt.Errorf("script %s: invalid phase %q", s.ID, s.Phase)
		}
		if _, exists := byID[s.ID]; exists {
			return nil, fmt.Errorf("duplicate script %q", s.ID)
		}
		byID[s.ID] = s
	}
	indegree, dependents := map[string]int{}, map[string][]string{}
	for _, s := range items {
		indegree[s.ID] = len(s.DependsOn)
		seen := map[string]bool{}
		for _, id := range s.DependsOn {
			d, ok := byID[id]
			if !ok {
				return nil, fmt.Errorf("script %s depends on missing script %s", s.ID, id)
			}
			if seen[id] {
				return nil, fmt.Errorf("script %s declares dependency %s more than once", s.ID, id)
			}
			if s.Phase == Pre && d.Phase == Post {
				return nil, fmt.Errorf("pre script %s cannot depend on post script %s", s.ID, id)
			}
			seen[id] = true
			dependents[id] = append(dependents[id], s.ID)
		}
	}
	ready := []string{}
	for id, degree := range indegree {
		if degree == 0 {
			ready = append(ready, id)
		}
	}
	ordered := make([]Script, 0, len(items))
	for len(ready) > 0 {
		slices.SortFunc(ready, func(a, b string) int {
			if byID[a].Phase != byID[b].Phase {
				if byID[a].Phase == Pre {
					return -1
				}
				return 1
			}
			if comparison := strings.Compare(byID[a].Name, byID[b].Name); comparison != 0 {
				return comparison
			}
			return strings.Compare(a, b)
		})
		id := ready[0]
		ready = ready[1:]
		ordered = append(ordered, byID[id])
		for _, next := range dependents[id] {
			indegree[next]--
			if indegree[next] == 0 {
				ready = append(ready, next)
			}
		}
	}
	if len(ordered) != len(items) {
		return nil, fmt.Errorf("SQL migration dependency cycle")
	}
	return ordered, nil
}
