// Package dialect is what the reviewed workflows need from the registered
// engine dialects besides planning: the desired schema a dialect reads from
// HCL, a schema snapshot, and the script a plan's statements make. Importing
// it registers the "sqlserver", "postgres" and "sqlite" dialects.
package dialect

import (
	"fmt"
	"slices"
	"strings"

	"ariga.io/atlas/sql/schema"
	atlashcl "github.com/flanksource/sqlglot-go/database/atlas/sqlserver"
	"github.com/flanksource/sqlglot-go/database/migrate/dialect/postgres"
	"github.com/flanksource/sqlglot-go/database/migrate/dialect/sqlite"
	"github.com/flanksource/sqlglot-go/database/migrate/dialect/sqlserver"
	"github.com/flanksource/sqlglot-go/migrate"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
)

var sqlServer = sqlserver.Dialect{}.Name()

// hclReaders is the one place a dialect name selects behaviour: each dialect
// evaluates its own HCL flavour to the schema the engine plans toward.
var hclReaders = map[string]func(hcl string) (migrate.Schema, error){
	sqlServer:     sqlServerFromHCL,
	postgres.Name: postgres.SchemaFromHCL,
	sqlite.Name:   sqlite.SchemaFromHCL,
}

// DesiredFromHCL evaluates declared HCL to the desired schema of a dialect.
func DesiredFromHCL(name, hcl string) (migrate.Schema, error) {
	read, ok := hclReaders[name]
	if !ok {
		known := make([]string, 0, len(hclReaders))
		for n := range hclReaders {
			known = append(known, n)
		}
		slices.Sort(known)
		return migrate.Schema{}, fmt.Errorf("dialect %q cannot read desired HCL (supported: %s)", name, strings.Join(known, ", "))
	}
	if strings.TrimSpace(hcl) == "" {
		return migrate.Schema{}, fmt.Errorf("desired %s HCL is empty", name)
	}
	return read(hcl)
}

func sqlServerFromHCL(hcl string) (migrate.Schema, error) {
	realm := &schema.Realm{}
	if err := atlashcl.EvalHCLBytes([]byte(hcl), realm, nil); err != nil {
		return migrate.Schema{}, fmt.Errorf("evaluate sqlserver HCL: %w", err)
	}
	return planner.FromAtlas(realm)
}
