package sqlserver

import (
	"slices"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// planDiff is one comparison of two schemas, kept so Lower can reach the Atlas
// changes behind each described change.
type planDiff struct {
	*described
	current, desired migrate.Schema
	forcing          []engine.Forcing
}

var _ engine.Diff = (*planDiff)(nil)

func (p *planDiff) Changes() []migrate.Change { return slices.Clone(p.set.Changes) }

func (p *planDiff) Warnings() []string { return slices.Clone(p.warnings) }
