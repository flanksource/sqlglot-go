//go:build integration

package migrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"testing/fstest"
	"time"

	_ "github.com/lib/pq"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// newPostgresDatabase creates a throwaway database on the server named by
// SQLGLOT_TEST_POSTGRES_URL and returns its connection string and a handle.
func newPostgresDatabase(ctx context.Context) (string, *sql.DB) {
	adminURL := os.Getenv("SQLGLOT_TEST_POSTGRES_URL")
	Expect(adminURL).NotTo(BeEmpty(), "integration tests require a disposable PostgreSQL server")
	admin, err := sql.Open("postgres", adminURL)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(admin.Close)
	name := fmt.Sprintf("sqlglot_migrate_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(ctx, "CREATE DATABASE "+name)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { _, e := admin.Exec("DROP DATABASE " + name + " WITH (FORCE)"); Expect(e).NotTo(HaveOccurred()) })
	u, err := url.Parse(adminURL)
	Expect(err).NotTo(HaveOccurred())
	u.Path = "/" + name
	db, err := sql.Open("postgres", u.String())
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(db.Close)
	return u.String(), db
}

func bundleFile(content string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(content)} }

// changeIDOfSchema is the ID of a change on a table of the public schema.
func changeIDOfSchema(action, kind, table, member string) string {
	id, err := json.Marshal([]string{action, kind, "public", table, member})
	Expect(err).NotTo(HaveOccurred())
	return string(id)
}

// scalarOf runs a query that returns one text value.
func scalarOf(ctx context.Context, db *sql.DB, query string) string {
	var value string
	Expect(db.QueryRowContext(ctx, query).Scan(&value)).To(Succeed(), query)
	return value
}

func mustExecAll(ctx context.Context, db *sql.DB, statements ...string) {
	for _, statement := range statements {
		_, err := db.ExecContext(ctx, statement)
		Expect(err).NotTo(HaveOccurred(), statement)
	}
}
