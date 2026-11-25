// Package sqlserver is the database half of the SQL Server dialect of the
// schema engine: it reads capabilities, schemas and the live dependents of
// columns, and runs a unit on one connection. Diffing, lowering and
// preconditions are the pure planner it embeds.
package sqlserver

import (
	"github.com/flanksource/sqlglot-go/migrate/engine"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
)

// Dialect is registered as "sqlserver".
type Dialect struct {
	planner.Planner
}

var _ engine.Dialect = Dialect{}

func init() { engine.Register(Dialect{}) }

// SameDependent judges a rebuilt dependent identical when what re-creates it is
// equal. Every definition is normalised where it is read, so a strict
// comparison is enough.
func (Dialect) SameDependent(before, after engine.Dependent) (bool, error) {
	return engine.SameDefinition(before, after)
}
