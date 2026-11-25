//go:build integration

package sqlserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver/sqlservertest"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// lockWait is how long a spec lets a unit wait for the migration lock.
const lockWait = 30 * time.Second

// db is the one disposable server, at compatibility level 100, every
// integration spec shares; each spec works in a schema of its own.
var db *sql.DB

var _ = BeforeSuite(func(ctx SpecContext) {
	db = sqlservertest.OpenDisposable(ctx)
	mustExec(ctx, db, "ALTER DATABASE CURRENT SET COMPATIBILITY_LEVEL = 100")
	Expect(scalar[int](ctx, db, "SELECT compatibility_level FROM sys.databases WHERE name = DB_NAME()")).To(Equal(100))
})

func mustExec(ctx context.Context, db *sql.DB, statements ...string) {
	GinkgoHelper()
	for _, s := range statements {
		_, err := db.ExecContext(ctx, s)
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), s)
	}
}

func scalar[T any](ctx context.Context, db *sql.DB, query string, args ...any) T {
	var v T
	ExpectWithOffset(1, db.QueryRowContext(ctx, query, args...).Scan(&v)).To(Succeed(), query)
	return v
}

func inspect(ctx context.Context, db *sql.DB, scope engine.Scope) engine.Inspection {
	got, err := Dialect{}.Inspect(ctx, db, scope)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return got
}

func tableOf(s migrate.Schema, name migrate.Name) *migrate.Table {
	for i := range s.Tables {
		if s.Tables[i].Name == name {
			return &s.Tables[i]
		}
	}
	Fail("table " + name.Schema + "." + name.Name + " is not in the schema")
	return nil
}

func column(t *migrate.Table, name string) *migrate.Column {
	for i := range t.Columns {
		if t.Columns[i].Name == name {
			return &t.Columns[i]
		}
	}
	Fail("column " + name + " is not in " + t.Name.Name)
	return nil
}

func cloneSchema(s migrate.Schema) migrate.Schema {
	body, err := json.Marshal(s)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	var out migrate.Schema
	ExpectWithOffset(1, json.Unmarshal(body, &out)).To(Succeed())
	return out
}

func declared(schema string, tables ...string) engine.Scope {
	scope := engine.Scope{Mode: engine.ScopeDeclared, Schemas: []string{schema}}
	for _, t := range tables {
		scope.Objects = append(scope.Objects, migrate.Name{Schema: schema, Name: t})
	}
	return scope
}

func planLive(ctx context.Context, db *sql.DB, scope engine.Scope, desired migrate.Schema) (engine.Artifact, error) {
	return engine.PlanLive(ctx, db, engine.PlanLiveRequest{Dialect: "sqlserver", Scope: scope, Desired: desired})
}

func applyArtifact(ctx context.Context, db *sql.DB, a engine.Artifact) (engine.Result, error) {
	return engine.Apply(ctx, db, engine.ApplyRequest{Artifact: a, Hash: a.Hash, Acknowledgements: engine.Acknowledgements(a), LockTimeout: lockWait})
}

func changeIDs(a engine.Artifact, action string) []string {
	var out []string
	for _, c := range a.Changes {
		if c.Action == action {
			out = append(out, c.ID)
		}
	}
	return out
}
