package sqlserver

import (
	"cmp"
	"slices"

	"ariga.io/atlas/sql/schema"
)

type tableStep struct {
	source schema.Change
	change schema.Change
	phase  int
}

func orderedTableChanges(changes []schema.Change) []tableStep {
	steps := make([]tableStep, 0, len(changes))
	for _, change := range changes {
		var drop, add schema.Change
		switch c := change.(type) {
		case *schema.ModifyIndex:
			if !fillFactorOnly(c.From, c.To) {
				drop, add = &schema.DropIndex{I: c.From}, &schema.AddIndex{I: c.To}
			}
		case *schema.ModifyPrimaryKey:
			if !fillFactorOnly(c.From, c.To) {
				drop, add = &schema.DropPrimaryKey{P: c.From}, &schema.AddPrimaryKey{P: c.To}
			}
		case *schema.ModifyForeignKey:
			drop, add = &schema.DropForeignKey{F: c.From}, &schema.AddForeignKey{F: c.To}
		case *schema.ModifyCheck:
			drop, add = &schema.DropCheck{C: c.From}, &schema.AddCheck{C: c.To}
		}
		if drop != nil {
			steps = append(steps, tableStep{source: change, change: drop, phase: 0}, tableStep{source: change, change: add, phase: 2})
			continue
		}
		phase := 1
		switch change.(type) {
		case *schema.DropIndex, *schema.DropPrimaryKey, *schema.DropForeignKey, *schema.DropCheck:
			phase = 0
		case *schema.AddIndex, *schema.AddPrimaryKey, *schema.AddForeignKey, *schema.AddCheck:
			phase = 2
		}
		steps = append(steps, tableStep{source: change, change: change, phase: phase})
	}
	slices.SortStableFunc(steps, func(a, b tableStep) int { return cmp.Compare(a.phase, b.phase) })
	return steps
}

// fillFactorOnly reports an index change that a rebuild applies in place,
// keeping the index instead of dropping and re-creating it.
func fillFactorOnly(from, to *schema.Index) bool {
	if from.Unique != to.Unique || IndexDefinitionChanged(from.Attrs, to.Attrs) || len(from.Parts) != len(to.Parts) {
		return false
	}
	for i := range from.Parts {
		if from.Parts[i].C == nil || to.Parts[i].C == nil || from.Parts[i].C.Name != to.Parts[i].C.Name || from.Parts[i].Desc != to.Parts[i].Desc {
			return false
		}
	}
	return true
}
