package migrate

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"

	_ "github.com/flanksource/sqlglot-go/database/migrate/dialect/sqlserver" // registers the "sqlserver" engine dialect
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
)

// PlanSchema is the reviewed schema plan of a SQL Server bundle on db: its HCL
// tables and WithRoutines routines against the live dbo schema, as the engine
// artifact Apply embeds in its plan file. It reads only the declared objects
// and what depends on the columns a change forces, never drops an object the
// bundle does not declare, and changes nothing. The bundle's scripts are not
// loaded. The caller owns db.
func PlanSchema(ctx context.Context, db *sql.DB, schemaFS fs.FS, opts ...Option) (engine.Artifact, error) {
	if db == nil {
		return engine.Artifact{}, errors.New("database is nil")
	}
	realm, err := sqlServerDesired(schemaFS, resolveOptions(opts))
	if err != nil {
		return engine.Artifact{}, err
	}
	return planSchemaArtifact(ctx, db, sqlServerBundle{realm: realm})
}

// planSchemaArtifact plans the bundle's declared tables and routines in one
// declared-scope batch, so that ordering, forced rebuilds and units span them all.
func planSchemaArtifact(ctx context.Context, db *sql.DB, bundle sqlServerBundle) (engine.Artifact, error) {
	if !bundle.declaresObjects() {
		return engine.Artifact{}, errors.New("SQL Server bundle declares no tables or routines to plan")
	}
	desired, err := planner.FromAtlas(bundle.realm)
	if err != nil {
		return engine.Artifact{}, err
	}
	return engine.PlanLive(ctx, db, engine.PlanLiveRequest{
		Dialect: "sqlserver",
		Scope:   engine.Scope{Mode: engine.ScopeDeclared, Schemas: []string{sqlServerSchema}, Objects: bundle.declaredNames()},
		Desired: desired,
	})
}

// declaredNames are the declared tables and routines as engine object names.
func (b sqlServerBundle) declaredNames() []migrate.Name {
	target := b.realm.Schemas[0]
	var names []migrate.Name
	for _, t := range target.Tables {
		names = append(names, migrate.Name{Schema: target.Name, Name: t.Name})
	}
	for _, p := range target.Procs {
		names = append(names, migrate.Name{Schema: target.Name, Name: p.Name})
	}
	for _, f := range target.Funcs {
		names = append(names, migrate.Name{Schema: target.Name, Name: f.Name})
	}
	return names
}
