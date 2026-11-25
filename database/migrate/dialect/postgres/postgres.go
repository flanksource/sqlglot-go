// Package postgres registers the "postgres" engine dialect, planned with the
// upstream Atlas PostgreSQL driver.
package postgres

import (
	"fmt"
	"strings"

	atlaspostgres "ariga.io/atlas/sql/postgres"
	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/database/migrate/dialect/atlasdriver"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/changeset"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// Name is the registered dialect name.
const Name = "postgres"

// Dialect is the registered PostgreSQL dialect.
var Dialect = atlasdriver.New(backend{})

func init() { engine.Register(Dialect) }

// SchemaFromHCL turns declared Atlas PostgreSQL HCL into the schema to plan toward.
func SchemaFromHCL(hcl string) (migrate.Schema, error) { return Dialect.SchemaFromHCL(hcl) }

// SchemaFromAtlas turns a schema evaluated from declared Atlas PostgreSQL HCL
// (with its variables resolved and its name remapped) into the schema to plan toward.
func SchemaFromAtlas(s *schema.Schema) (migrate.Schema, error) { return Dialect.SchemaFromAtlas(s) }

type backend struct{}

func (backend) Name() string { return Name }

// OfflineCapabilities: PostgreSQL DDL is transactional on every server.
func (backend) OfflineCapabilities() engine.Capabilities {
	return engine.Capabilities{TransactionalDDL: true}
}

func (backend) Atlas() atlasdriver.Atlas {
	return atlasdriver.Atlas{
		Marshal: atlaspostgres.MarshalHCL, Eval: atlaspostgres.EvalHCL,
		Differ: atlaspostgres.DefaultDiff, Planner: atlaspostgres.DefaultPlan,
		Open: atlaspostgres.Open, FormatType: atlaspostgres.FormatType, IndexFilter: indexPredicate,
	}
}

func (backend) Changes() changeset.Dialect { return atlasdriver.BaseChanges(Name) }

func (backend) ValidateSchema(name string) error {
	if name == "" || strings.HasPrefix(name, "pg_") || name == "information_schema" {
		return fmt.Errorf("system or empty schema %q cannot be migrated", name)
	}
	return nil
}

func indexPredicate(attrs []schema.Attr) string {
	for _, a := range attrs {
		if p, ok := a.(*atlaspostgres.IndexPredicate); ok {
			return p.P
		}
	}
	return ""
}
