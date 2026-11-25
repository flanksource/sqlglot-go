package postgres

import (
	"fmt"
	"strings"

	"github.com/flanksource/sqlglot-go/database/migrate/dialect/atlasdriver"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// bodyMarker ends a view definition's header (CREATE VIEW name (columns)) and
// starts its stored query.
const bodyMarker = " AS\n"

// SameDependent decides whether a rebuilt dependent is back as it was. A view
// is compared on everything but the stored query text: kind, name, column
// list, owner, options, indexes, comment and grants. PostgreSQL prints a
// view's query from its parse tree, so a retype that makes a cast redundant
// (a varchar column read as text) changes the text of the very same view; the
// text that was re-created is the one the plan recorded, so comparing it again
// can only report that normalisation. Establishing "differs only by a
// redundant cast" safely is not cheap, so the body is ignored rather than
// guessed at. Every other kind uses the strict engine comparison.
func (backend) SameDependent(before, after engine.Dependent) (bool, error) {
	if before.Kind != engine.DependentView || after.Kind != engine.DependentView {
		return engine.SameDefinition(before, after)
	}
	left, err := viewShape(before)
	if err != nil {
		return false, err
	}
	right, err := viewShape(after)
	if err != nil {
		return false, err
	}
	return left == right, nil
}

// viewShape is the definition without its query text.
func viewShape(d engine.Dependent) (string, error) {
	header, rest, ok := strings.Cut(d.Definition, bodyMarker)
	if !ok {
		return "", fmt.Errorf("postgres: view %s has an unrecognised definition: no %q", d.Table.Name, strings.TrimSpace(bodyMarker))
	}
	keyword := "VIEW"
	if strings.HasPrefix(header, "CREATE MATERIALIZED VIEW") {
		keyword = "MATERIALIZED VIEW"
	}
	owner := fmt.Sprintf(";\nALTER %s %s OWNER TO ", keyword, atlasdriver.NameString(d.Table))
	_, tail, ok := strings.Cut(rest, owner)
	if !ok {
		return "", fmt.Errorf("postgres: view %s has an unrecognised definition: no owner statement", d.Table.Name)
	}
	return header + "\x00" + tail, nil
}
