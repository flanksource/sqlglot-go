package sqlserver_test

import (
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL Server object namespaces", func() {
	It("rejects a selected default name that conflicts with an unselected table constraint", func() {
		orders := migrate.Table{Name: migrate.Name{Schema: "dbo", Name: "orders"}, Columns: []migrate.Column{{Name: "id", Type: "int", Default: &migrate.Default{Name: "DF_shared", Expression: "(0)"}}}}
		audit := migrate.Table{Name: migrate.Name{Schema: "dbo", Name: "audit"}, Columns: []migrate.Column{{Name: "id", Type: "int"}}, Checks: []migrate.Check{{Name: "DF_shared", Expression: "id >= 0"}}}
		_, err := sqlserver.Plan(migrate.Request{Desired: migrate.Schema{Tables: []migrate.Table{orders, audit}}})
		Expect(err).To(MatchError(ContainSubstring("duplicate object")))
	})
	It("permits ordinary index names repeated on different tables", func() {
		orders := migrate.Table{Name: migrate.Name{Schema: "dbo", Name: "orders"}, Columns: []migrate.Column{{Name: "id", Type: "int"}}, Indexes: []migrate.Index{{Name: "IX_id", Columns: []migrate.IndexColumn{{Name: "id"}}}}}
		audit := orders
		audit.Name.Name = "audit"
		_, err := sqlserver.Plan(migrate.Request{Desired: migrate.Schema{Tables: []migrate.Table{orders, audit}}})
		Expect(err).NotTo(HaveOccurred())
	})
})
