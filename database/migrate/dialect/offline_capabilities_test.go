package dialect_test

import (
	"context"

	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	// These imports register the dialects under test.
	_ "github.com/flanksource/sqlglot-go/database/migrate/dialect/postgres"
	_ "github.com/flanksource/sqlglot-go/database/migrate/dialect/sqlite"
	_ "github.com/flanksource/sqlglot-go/database/migrate/dialect/sqlserver"
)

var _ = Describe("a dialect's offline capabilities", func() {
	DescribeTable("have transactional DDL, no fingerprint and no version-gated feature",
		func(name string) {
			d, err := engine.Lookup(name)
			Expect(err).NotTo(HaveOccurred())
			caps, err := d.Capabilities(context.Background(), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(caps).To(Equal(engine.Capabilities{TransactionalDDL: true}))
		},
		Entry("SQL Server", "sqlserver"),
		Entry("PostgreSQL", "postgres"),
		Entry("SQLite", "sqlite"),
	)
})
