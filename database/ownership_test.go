package database_test

import (
	"context"
	"database/sql"
	"testing"

	inspection "github.com/flanksource/sqlglot-go/database/inspect"
	"github.com/flanksource/sqlglot-go/database/migrate"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	_ "modernc.org/sqlite"
)

func TestDatabase(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Standalone database") }

var _ = Describe("standalone database ownership", func() {
	It("inspects SQLite without a commons-db connection or cache", func() {
		db, err := sql.Open("sqlite", ":memory:")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		_, err = db.Exec("CREATE TABLE example (id integer PRIMARY KEY)")
		Expect(err).NotTo(HaveOccurred())
		catalog, err := inspection.Inspect(context.Background(), db, "sqlite", inspection.Limits{})
		Expect(err).NotTo(HaveOccurred())
		Expect(catalog.Schemas).To(HaveLen(1))
		Expect(catalog.Schemas[0].Relations[0].Name).To(Equal("example"))
	})
	It("retains the standalone bundle apply API", func() {
		Expect(migrate.Apply(context.Background(), "", nil)).To(MatchError(ContainSubstring("connection")))
	})
})
