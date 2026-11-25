package compare

import (
	dialects "github.com/flanksource/sqlglot-go/database/migrate/dialect"
	"github.com/flanksource/sqlglot-go/migrate"
)

var Label = label

func SQLServerObjects(current, desired migrate.Schema, changes []migrate.Change) ([]Object, error) {
	return (&sides{current: dialects.Snapshot{Schema: current}, reference: dialects.Snapshot{Schema: desired}}).objects(changes)
}
