//go:build integration

package proxy_test

import (
	"database/sql"
	"github.com/flanksource/sqlglot-go/proxy"
	"github.com/flanksource/sqlglot-go/proxy/postgres"
	"github.com/flanksource/sqlglot-go/proxy/tds"
	_ "github.com/lib/pq"
	_ "github.com/microsoft/go-mssqldb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"net/url"
	"os"
)

var _ = Describe("Disposable database integration", func() {
	DescribeTable("preserves results through the wire proxy", func(ctx SpecContext, driver, variable string) {
		raw := os.Getenv(variable)
		Expect(raw).NotTo(BeEmpty(), "provide a disposable database URL")
		upstream, err := url.Parse(raw)
		Expect(err).NotTo(HaveOccurred())
		direct, err := sql.Open(driver, raw)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(direct.Close)
		var expected int
		Expect(direct.QueryRowContext(ctx, "SELECT CAST(7 AS INT)").Scan(&expected)).To(Succeed())
		rows := make(chan proxy.Statement, 32)
		opts := proxy.Options{Upstream: upstream.Host, OnStatement: func(row proxy.Statement) error { rows <- row; return nil }}
		start := postgres.Start
		if driver == "sqlserver" {
			start = tds.Start
		}
		server, err := start(ctx, opts)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(server.Close)
		upstream.Host = server.Addr()
		params := upstream.Query()
		if driver == "postgres" {
			params.Set("sslmode", "disable")
		} else {
			params.Set("encrypt", "disable")
		}
		upstream.RawQuery = params.Encode()
		db, err := sql.Open(driver, upstream.String())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		var actual int
		Expect(db.QueryRowContext(ctx, "SELECT CAST(7 AS INT)").Scan(&actual)).To(Succeed())
		Expect(actual).To(Equal(expected))
		var captured proxy.Statement
		Eventually(rows).Should(Receive(&captured))
		Expect(captured.SQL).To(Equal("SELECT CAST(7 AS INT)"))
		Expect(captured.Error).To(BeEmpty())
	}, Entry("PostgreSQL", "postgres", "SQLGLOT_PROXY_POSTGRES_URL"), Entry("SQL Server", "sqlserver", "SQLGLOT_PROXY_SQLSERVER_URL"))
})
