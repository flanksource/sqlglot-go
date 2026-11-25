package migrate

import (
	"fmt"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// changeSelection limits a plan to some of its changes. The zero value plans
// every change.
type changeSelection struct {
	IDs []string
	// None plans no change (IDs is empty).
	None bool
}

// unreviewedSelection is what an unreviewed apply runs of a plan. The engine
// plans every drop of a column a complete declaration omits; an unreviewed
// apply has nobody to acknowledge them, and a column holds data, so it runs
// every other planned change (the indexes, keys and checks a table lost hold
// none) and leaves the column drops out, reporting each one.
type unreviewedSelection struct {
	// Artifact is the plan to run; it holds no change when nothing is left to run.
	Artifact *engine.Artifact
	// Warnings name each column the plan leaves in place.
	Warnings []string
}

// selectUnreviewed leaves the planned column drops out of full by planning again
// with the selection of every other change. replan plans with a selection; when
// leaving a drop out makes a planned change unappliable its error is returned
// with the engine's message, because the engine fails closed on a change that
// requires an unselected one. canDrop says whether the caller can ask for the
// drops (WithDrops), for the warning's advice.
func selectUnreviewed(full engine.Artifact, canDrop bool, replan func(changeSelection) (engine.Artifact, error)) (unreviewedSelection, error) {
	var kept []string
	var left []migrate.Change
	for _, c := range full.Changes {
		switch {
		case c.Action == "rebuild":
			continue
		case c.Action == "drop" && c.Kind == "column":
			left = append(left, c)
		default:
			kept = append(kept, c.ID)
		}
	}
	if len(left) == 0 {
		return unreviewedSelection{Artifact: &full}, nil
	}
	warnings := columnDropWarnings(left, canDrop)
	artifact, err := replan(changeSelection{IDs: kept, None: len(kept) == 0})
	if err != nil {
		return unreviewedSelection{}, fmt.Errorf("leaving out the column drops (%s): %w", strings.Join(warnings, "; "), err)
	}
	return unreviewedSelection{Artifact: &artifact, Warnings: warnings}, nil
}

// columnDropWarnings has one warning per column left in place.
func columnDropWarnings(drops []migrate.Change, canDrop bool) []string {
	out := make([]string, len(drops))
	for i, c := range drops {
		table := c.Object.Schema + "." + c.Object.Name
		options := "declare the column, mark the table `partial = true`, or apply with WithDrops"
		if !canDrop {
			options = "declare the column or mark the table `partial = true`; SQL Server bundles do not support WithDrops"
		}
		out[i] = fmt.Sprintf("column %q of table %s is in the database but not declared, and was not dropped: %s", c.Member, table, options)
	}
	return out
}
