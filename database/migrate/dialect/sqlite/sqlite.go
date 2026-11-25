// Package sqlite registers the "sqlite" engine dialect, planned with the
// upstream Atlas SQLite driver.
package sqlite

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"ariga.io/atlas/sql/schema"
	atlassqlite "ariga.io/atlas/sql/sqlite"
	"github.com/flanksource/sqlglot-go/database/migrate/dialect/atlasdriver"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/changeset"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

const (
	// Name is the registered dialect name.
	Name = "sqlite"
	// SchemaName is the only schema SQLite migrations manage.
	SchemaName = "main"
)

// Dialect is the registered SQLite dialect.
var Dialect = atlasdriver.New(backend{})

func init() { engine.Register(Dialect) }

// SchemaFromHCL turns declared Atlas SQLite HCL into the schema to plan toward.
func SchemaFromHCL(hcl string) (migrate.Schema, error) { return Dialect.SchemaFromHCL(hcl) }

// SchemaFromAtlas turns a schema already projected to SQLite (types, checks,
// the main schema name) into the schema to plan toward.
func SchemaFromAtlas(s *schema.Schema) (migrate.Schema, error) { return Dialect.SchemaFromAtlas(s) }

type backend struct{}

// Reconcile is the identity: Atlas's SQLite differ compares defaults,
// collations and types as text without asking the database, so there is no
// spelling the database could consider equal that the differ would not.
func (backend) Reconcile(context.Context, engine.Querier, *schema.Schema, *schema.Schema, func(string) bool) (bool, error) {
	return false, nil
}

// SameDependent is the strict comparison: a SQLite view is re-created from the
// text the database stored, which it stores verbatim.
func (backend) SameDependent(before, after engine.Dependent) (bool, error) {
	return engine.SameDefinition(before, after)
}

func (backend) Name() string { return Name }

// OfflineCapabilities: SQLite DDL is transactional on every server.
func (backend) OfflineCapabilities() engine.Capabilities {
	return engine.Capabilities{TransactionalDDL: true}
}

func (backend) Atlas() atlasdriver.Atlas {
	return atlasdriver.Atlas{
		Marshal: atlassqlite.MarshalHCL, Eval: atlassqlite.EvalHCL,
		Differ: atlassqlite.DefaultDiff, Planner: atlassqlite.DefaultPlan,
		Open: atlassqlite.Open, FormatType: atlassqlite.FormatType, IndexFilter: indexPredicate,
	}
}

func (backend) ValidateSchema(name string) error {
	if name != SchemaName {
		return fmt.Errorf("SQLite migrations require schema %q, got %q", SchemaName, name)
	}
	return nil
}

func (backend) Changes() changeset.Dialect {
	d := atlasdriver.BaseChanges(Name)
	d.DefaultSchema, d.Rebuilds = SchemaName, rebuilds
	return d
}

func indexPredicate(attrs []schema.Attr) string {
	for _, a := range attrs {
		if p, ok := a.(*atlassqlite.IndexPredicate); ok {
			return p.P
		}
	}
	return ""
}

// rebuilds mirrors Atlas's SQLite alterable rule for a single member change:
// anything but index changes and a plain column addition rebuilds the table
// from its full desired definition, so it carries every sibling change along.
func rebuilds(c schema.Change) bool {
	switch c := c.(type) {
	case *schema.RenameColumn, *schema.RenameIndex, *schema.DropIndex, *schema.AddIndex:
		return false
	case *schema.AddColumn:
		if len(c.C.Indexes) > 0 || len(c.C.ForeignKeys) > 0 {
			return true
		}
		switch x := c.C.Default.(type) {
		case *schema.Literal:
			return slices.Contains([]string{"CURRENT_TIME", "CURRENT_DATE", "CURRENT_TIMESTAMP"}, x.V)
		case *schema.RawExpr:
			return true
		}
		return slices.ContainsFunc(c.C.Attrs, func(a schema.Attr) bool {
			g, ok := a.(*schema.GeneratedExpr)
			return ok && strings.EqualFold(g.Type, "STORED")
		})
	}
	return true
}
