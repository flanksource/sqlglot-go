package sqlserver

import (
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
)

// planTotals counts what a plan does.
type planTotals struct {
	run, rerun, blocked, unchanged, statements, destructive int
}

// Pretty renders the plan terraform-style: each phase's scripts as + run
// (new), ~ re-run with its reasons, ! blocked and ? effects unknown, the schema
// step between the phases, the warnings and the totals.
func (p PlanFile) Pretty() api.Text {
	var totals planTotals
	out := api.Text{}.Append("Plan for ", "font-bold").Append(p.Target.Server+"/"+p.Target.Database, "font-bold").
		Append(" (scope "+p.Target.Scope+")", "text-gray-500")
	if len(p.Absorbed) > 0 {
		out = out.Append(" absorbing "+strings.Join(p.Absorbed, ", "), "text-gray-500")
	}
	out = p.prettyFunctions(out)
	out = p.prettyPhase(out, scripts.Pre, "Pre-deployment scripts", &totals)
	out = p.prettySchema(out, &totals)
	out = p.prettyPhase(out, scripts.Post, "Post-deployment scripts", &totals)
	if len(p.Warnings) > 0 {
		out = out.NewLine().NewLine().Append("Warnings", "font-bold")
		for _, warning := range p.Warnings {
			out = out.NewLine().Append("  ! ", "text-yellow-600").Append(warning)
		}
	}
	return out.NewLine().NewLine().Append(totals.String(), "font-bold")
}

// prettyFunctions renders each function the plan selects scripts by: its value,
// what it was and how many of its scripts that change selects.
func (p PlanFile) prettyFunctions(out api.Text) api.Text {
	functions := p.Functions()
	if len(functions) == 0 {
		return out
	}
	out = out.NewLine().NewLine().Append("Functions", "font-bold")
	for _, f := range functions {
		changed := 0
		var previous []string
		for _, use := range f.Uses {
			if !use.ValueChanged && !use.DefinitionChanged {
				continue
			}
			changed++
			if use.ValueChanged && use.Recorded {
				previous = append(previous, displayValue(use.Previous))
			}
		}
		marker, style := "  = ", "text-gray-500"
		if changed > 0 {
			marker, style = "  ~ ", "text-yellow-600"
		}
		out = out.NewLine().Append(marker, style).Append(f.Display())
		if f.Sampled {
			out = out.Append(" = " + displayValue(f.Value))
		}
		slices.Sort(previous)
		if previous = slices.Compact(previous); len(previous) > 0 {
			out = out.Append(" (was "+strings.Join(previous, ", ")+")", "text-yellow-600")
		}
		detail := fmt.Sprintf("  used by %d", len(f.Uses))
		if changed > 0 {
			detail += fmt.Sprintf(", changed for %d", changed)
		}
		out = out.Append(detail, "text-gray-500")
	}
	return out
}

func displayValue(value *string) string {
	if value == nil {
		return "NULL"
	}
	return fmt.Sprintf("%q", *value)
}

func (t planTotals) String() string {
	summary := fmt.Sprintf("Plan: %d to run, %d to re-run, %d blocked, %d unchanged", t.run, t.rerun, t.blocked, t.unchanged)
	if t.statements > 0 {
		summary += fmt.Sprintf(", %d schema statements (%d destructive)", t.statements, t.destructive)
	}
	return summary + "."
}

func (p PlanFile) prettyPhase(out api.Text, phase scripts.Phase, title string, totals *planTotals) api.Text {
	listed := false
	for _, item := range p.Actions {
		if item.Script.Phase != phase {
			continue
		}
		if !item.Selected && len(item.Blocked) == 0 {
			totals.unchanged++
			continue
		}
		if !listed {
			out, listed = out.NewLine().NewLine().Append(title, "font-bold"), true
		}
		out = p.prettyAction(out.NewLine(), item, totals)
	}
	return out
}

func (p PlanFile) prettyAction(out api.Text, item scripts.Pending, totals *planTotals) api.Text {
	switch {
	case len(item.Blocked) > 0:
		totals.blocked++
		out = out.Append("  ! blocked  ", "text-red-600").Append(item.Script.ID).Append("  "+strings.Join(item.Blocked, "; "), "text-gray-500")
	case slices.Contains(item.Reasons, "new"):
		totals.run++
		out = out.Append("  + run      ", "text-green-600").Append(item.Script.ID).Append("  "+strings.Join(item.Reasons, ", "), "text-gray-500")
	default:
		totals.rerun++
		out = out.Append("  ~ re-run   ", "text-yellow-600").Append(item.Script.ID).Append("  "+strings.Join(item.Reasons, ", "), "text-gray-500")
	}
	if item.Script.NonTransactional && item.Selected {
		out = out.Append("  (nontransactional)", "text-yellow-600")
	}
	for _, read := range item.Reads {
		if read.Changed {
			out = out.NewLine().Append("      ~ reads ", "text-yellow-600").Append(strings.Join(strings.Fields(read.SQL), " ")).
				Append("  "+read.Change(), "text-yellow-600")
		}
	}
	for _, unknown := range item.EffectsUnknown {
		out = out.NewLine().Append("      ? effects unknown ", "text-purple-600").Append(unknown, "text-gray-500")
	}
	return out
}

func (p PlanFile) prettySchema(out api.Text, totals *planTotals) api.Text {
	step := p.Bundle.Schema
	if step == nil {
		return out
	}
	out = out.NewLine().NewLine().Append("Schema", "font-bold").Append(fmt.Sprintf(" (%d declared objects)", len(step.Objects)), "text-gray-500")
	if len(step.Statements) == 0 {
		return out.NewLine().Append("  = declared objects match", "text-gray-500")
	}
	for _, statement := range step.Statements {
		totals.statements++
		marker, style := "  ~ ", "text-yellow-600"
		if statement.Destructive {
			totals.destructive++
			marker, style = "  ! destructive ", "text-red-600"
		}
		out = out.NewLine().Append(marker, style).Append(strings.TrimSpace(statement.SQL))
	}
	return out
}
