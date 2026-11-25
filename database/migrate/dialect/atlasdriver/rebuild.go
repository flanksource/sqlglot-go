package atlasdriver

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

const (
	createMaterialized = "CREATE MATERIALIZED VIEW"
	idStart            = `(^|[^A-Za-z0-9_$])`
	idEnd              = `([^A-Za-z0-9_$]|$)`
)

// lowerRebuilds drops and re-creates the dependent views a forcing change
// invalidates. Views are dropped outermost first and re-created innermost
// first: a view that names another in its definition is built on it.
func (p *planDiff) lowerRebuilds(rebuilds []engine.Rebuild) ([]engine.Op, error) {
	drops := make([]engine.Op, len(rebuilds))
	creates := make([]engine.Op, len(rebuilds))
	for i, r := range rebuilds {
		if r.Dependent.Kind != engine.DependentView {
			return nil, fmt.Errorf("%s: cannot rebuild %s %s: only views are rebuilt", p.d.Name(), r.Dependent.Kind, r.Change.ID)
		}
		if strings.TrimSpace(r.Dependent.Definition) == "" {
			return nil, fmt.Errorf("%s: dependent view %s has no definition to re-create", p.d.Name(), nameString(r.Dependent.Table))
		}
		drops[i], creates[i] = viewOps(r)
	}
	for i, r := range rebuilds {
		for j, other := range rebuilds {
			if i != j && mentions(r.Dependent.Definition, other.Dependent.Table.Name) {
				drops[j].Requires = append(drops[j].Requires, drops[i].ID)
				creates[i].Requires = append(creates[i].Requires, creates[j].ID)
			}
		}
	}
	return append(drops, creates...), nil
}

func viewOps(r engine.Rebuild) (drop, create engine.Op) {
	keyword := "VIEW"
	if strings.HasPrefix(r.Dependent.Definition, createMaterialized) {
		keyword = "MATERIALIZED VIEW"
	}
	drop = engine.Op{
		ID: r.Change.ID + "#drop", Change: r.Change.ID, Phase: engine.PhaseDrop, Object: r.Change.Object,
		Statements: []engine.Statement{{SQL: fmt.Sprintf("DROP %s %s", keyword, nameString(r.Dependent.Table))}},
	}
	create = engine.Op{
		ID: r.Change.ID + "#create", Change: r.Change.ID, Phase: engine.PhaseCreate, Object: r.Change.Object,
		Requires:   []string{drop.ID},
		Statements: []engine.Statement{{SQL: r.Dependent.Definition}},
	}
	return drop, create
}

// NameString renders a schema-qualified, quoted object name.
func NameString(n migrate.Name) string { return nameString(n) }

func nameString(n migrate.Name) string { return quoteIdent(n.Schema) + "." + quoteIdent(n.Name) }

// QuoteIdent quotes an identifier for every Atlas dialect (double quotes).
func QuoteIdent(s string) string { return quoteIdent(s) }

// Mentions reports whether definition uses name as a whole identifier.
func Mentions(definition, name string) bool { return mentions(definition, name) }

func mentions(definition, name string) bool {
	re := regexp.MustCompile(`(?i)` + idStart + regexp.QuoteMeta(name) + idEnd)
	return re.MatchString(definition)
}
