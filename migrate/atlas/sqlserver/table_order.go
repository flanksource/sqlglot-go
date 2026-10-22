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
			drop, add = &schema.DropIndex{I: c.From}, &schema.AddIndex{I: c.To}
		case *schema.ModifyPrimaryKey:
			drop, add = &schema.DropPrimaryKey{P: c.From}, &schema.AddPrimaryKey{P: c.To}
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
