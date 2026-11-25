//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/flanksource/sqlglot-go/database/migrate/dialect/postgres"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	_ "github.com/lib/pq"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPostgresDialect(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Postgres engine dialect")
}

const schemaName = "public"

// openDB creates a throwaway database on the server named by SQLGLOT_TEST_POSTGRES_URL.
func openDB(ctx context.Context) *sql.DB {
	dsn := os.Getenv("SQLGLOT_TEST_POSTGRES_URL")
	Expect(dsn).NotTo(BeEmpty(), "integration tests require a disposable PostgreSQL server")
	admin, err := sql.Open("postgres", dsn)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(admin.Close)
	name := fmt.Sprintf("sqlglot_engine_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(ctx, "CREATE DATABASE "+name)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() {
		_, e := admin.Exec("DROP DATABASE " + name + " WITH (FORCE)")
		Expect(e).NotTo(HaveOccurred())
	})
	u, err := url.Parse(dsn)
	Expect(err).NotTo(HaveOccurred())
	u.Path = "/" + name
	db, err := sql.Open("postgres", u.String())
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(db.Close)
	return db
}

func mustExec(db *sql.DB, statements ...string) {
	for _, s := range statements {
		_, err := db.Exec(s)
		Expect(err).NotTo(HaveOccurred(), s)
	}
}

func desiredOf(hcl string) migrate.Schema {
	s, err := postgres.SchemaFromHCL(hcl)
	Expect(err).NotTo(HaveOccurred())
	return s
}

func scope(mode engine.ScopeMode, tables ...string) engine.Scope {
	sc := engine.Scope{Mode: mode, Schemas: []string{schemaName}}
	for _, t := range tables {
		sc.Objects = append(sc.Objects, migrate.Name{Schema: schemaName, Name: t})
	}
	return sc
}

func planLive(ctx context.Context, db *sql.DB, sc engine.Scope, hcl string) (engine.Artifact, error) {
	return engine.PlanLive(ctx, db, engine.PlanLiveRequest{Dialect: postgres.Name, Scope: sc, Desired: desiredOf(hcl)})
}

func mustPlan(ctx context.Context, db *sql.DB, sc engine.Scope, hcl string) engine.Artifact {
	a, err := planLive(ctx, db, sc, hcl)
	Expect(err).NotTo(HaveOccurred())
	return a
}

func apply(ctx context.Context, db *sql.DB, a engine.Artifact, lock time.Duration) (engine.Result, error) {
	return engine.Apply(ctx, db, engine.ApplyRequest{Artifact: a, Hash: a.Hash, Acknowledgements: engine.Acknowledgements(a), LockTimeout: lock})
}

func mustApply(ctx context.Context, db *sql.DB, a engine.Artifact) engine.Result {
	r, err := apply(ctx, db, a, 0)
	Expect(err).NotTo(HaveOccurred())
	return r
}

func changeID(action, kind, table, member string) string {
	id, err := json.Marshal([]string{action, kind, schemaName, table, member})
	Expect(err).NotTo(HaveOccurred())
	return string(id)
}

func statements(a engine.Artifact) []string {
	out := []string{}
	for _, u := range a.Units {
		for _, op := range u.Ops {
			for _, s := range op.Statements {
				out = append(out, s.SQL)
			}
		}
	}
	return out
}
