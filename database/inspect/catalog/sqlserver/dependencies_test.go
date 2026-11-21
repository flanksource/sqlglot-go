package mssqlcatalog_test

import (
	"database/sql/driver"
	"fmt"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/flanksource/sqlglot-go/database/inspect/catalog/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var moduleColumns = []string{"requested", "schema", "name", "kind", "definition", "ansi", "quoted", "type", "length", "precision", "scale", "parameters"}

var _ = Describe("SQL Server module batch reads", func() {
	var (
		mock    sqlmock.Sqlmock
		queryer mssqlcatalog.Queryer
	)

	BeforeEach(func() {
		db, m, err := sqlmock.New()
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() {
			Expect(m.ExpectationsWereMet()).To(Succeed())
			m.ExpectClose()
			Expect(db.Close()).To(Succeed())
		})
		mock, queryer = m, mssqlcatalog.FromDB(db)
	})

	It("reads every requested module in one VALUES join, keyed by the requested name", func() {
		const country, region, missing = "[dbo].[country]", "[dbo].[region]", "[dbo].[absent]"
		mock.ExpectQuery(`FROM \(VALUES \(@p1\), \(@p2\), \(@p3\)\) v\(name\) JOIN sys\.objects o ON o\.object_id=OBJECT_ID\(v\.name\)`).
			WithArgs(country, region, missing).
			WillReturnRows(sqlmock.NewRows(moduleColumns).
				AddRow(country, "dbo", "country", "FN", "CREATE FUNCTION dbo.country() RETURNS nvarchar(40) AS BEGIN RETURN N'ZW' END", true, true, "nvarchar", 80, 0, 0, 0).
				AddRow(region, "dbo", "region", "FS", nil, nil, nil, "nvarchar", 80, 0, 0, 1))

		modules, err := mssqlcatalog.ReadModules(GinkgoT().Context(), queryer, []string{country, region, missing})

		Expect(err).ToNot(HaveOccurred())
		Expect(modules).To(Equal(map[string]mssqlcatalog.Module{
			country: {Schema: "dbo", Name: "country", Kind: "FN", Definition: "CREATE FUNCTION dbo.country() RETURNS nvarchar(40) AS BEGIN RETURN N'ZW' END", ANSI: true, Quoted: true, ReturnType: "nvarchar", Length: 80},
			region:  {Schema: "dbo", Name: "region", Kind: "FS", ReturnType: "nvarchar", Length: 80, Parameters: 1, Unavailable: true},
		}))
	})

	It("chunks a large name list under the SQL Server parameter limit", func() {
		const total, chunk = 1500, 1000
		names := make([]string, total)
		for i := range names {
			names[i] = fmt.Sprintf("[dbo].[module_%d]", i)
		}
		for _, size := range []int{chunk, total - chunk} {
			args := make([]driver.Value, size)
			for i := range args {
				args[i] = sqlmock.AnyArg()
			}
			mock.ExpectQuery(`FROM \(VALUES`).WithArgs(args...).WillReturnRows(sqlmock.NewRows(moduleColumns))
		}

		modules, err := mssqlcatalog.ReadModules(GinkgoT().Context(), queryer, names)

		Expect(err).ToNot(HaveOccurred())
		Expect(modules).To(BeEmpty())
	})

	It("groups references by the requested referencing module in catalog order", func() {
		const country, region = "[dbo].[country]", "[dbo].[region]"
		mock.ExpectQuery(`FROM \(VALUES \(@p1\), \(@p2\)\) v\(name\) JOIN sys\.sql_expression_dependencies d ON d\.referencing_id=OBJECT_ID\(v\.name\)`).
			WithArgs(country, region).
			WillReturnRows(sqlmock.NewRows([]string{"requested", "server", "db", "schema", "name", "kind", "bound"}).
				AddRow(country, "", "", "dbo", "helper", "FN", true).
				AddRow(country, "", "", "dbo", "AsEnv", "U", true).
				AddRow(region, "", "remote", "dbo", "lookup", "", false))

		refs, err := mssqlcatalog.ReadModuleReferences(GinkgoT().Context(), queryer, []string{country, region})

		Expect(err).ToNot(HaveOccurred())
		Expect(refs).To(Equal(map[string][]mssqlcatalog.ModuleReference{
			country: {{Schema: "dbo", Name: "helper", Kind: "FN", Bound: true}, {Schema: "dbo", Name: "AsEnv", Kind: "U", Bound: true}},
			region:  {{Database: "remote", Schema: "dbo", Name: "lookup"}},
		}))
	})
})
