package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/flanksource/sqlglot-go/database/migrate/dialect/sqlite"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	_ "modernc.org/sqlite"
)

func TestSQLiteDialect(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SQLite engine dialect")
}

func openDB(dsnSuffix string) *sql.DB {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(GinkgoT().TempDir(), "engine.db")+dsnSuffix)
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
	s, err := sqlite.SchemaFromHCL(hcl)
	Expect(err).NotTo(HaveOccurred())
	return s
}

func completeScope() engine.Scope {
	return engine.Scope{Mode: engine.ScopeComplete, Schemas: []string{sqlite.SchemaName}}
}

func declaredScope(tables ...string) engine.Scope {
	scope := engine.Scope{Mode: engine.ScopeDeclared, Schemas: []string{sqlite.SchemaName}}
	for _, t := range tables {
		scope.Objects = append(scope.Objects, migrate.Name{Schema: sqlite.SchemaName, Name: t})
	}
	return scope
}

func planLive(ctx context.Context, db *sql.DB, scope engine.Scope, desired migrate.Schema, selection ...string) (engine.Artifact, error) {
	return engine.PlanLive(ctx, db, engine.PlanLiveRequest{Dialect: sqlite.Name, Scope: scope, Desired: desired, Selection: selection})
}

func mustPlan(ctx context.Context, db *sql.DB, scope engine.Scope, hcl string, selection ...string) engine.Artifact {
	a, err := planLive(ctx, db, scope, desiredOf(hcl), selection...)
	Expect(err).NotTo(HaveOccurred())
	return a
}

func apply(ctx context.Context, db *sql.DB, a engine.Artifact, acknowledgements ...string) (engine.Result, error) {
	return engine.Apply(ctx, db, engine.ApplyRequest{Artifact: a, Hash: a.Hash, Acknowledgements: acknowledgements})
}

func mustApply(ctx context.Context, db *sql.DB, a engine.Artifact, acknowledgements ...string) engine.Result {
	r, err := apply(ctx, db, a, acknowledgements...)
	Expect(err).NotTo(HaveOccurred())
	return r
}

func changeID(action, kind, table, member string) string {
	id, err := json.Marshal([]string{action, kind, sqlite.SchemaName, table, member})
	Expect(err).NotTo(HaveOccurred())
	return string(id)
}

func changeIDs(a engine.Artifact) []string {
	ids := []string{}
	for _, c := range a.Changes {
		ids = append(ids, c.ID)
	}
	return ids
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

func columnNames(db *sql.DB, table string) []string {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(rows.Close()).To(Succeed()) }()
	var names []string
	for rows.Next() {
		var n string
		Expect(rows.Scan(&n)).To(Succeed())
		names = append(names, n)
	}
	Expect(rows.Err()).NotTo(HaveOccurred())
	return names
}
