package sqlserver

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSQLServerScripts(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SQL Server script backend")
}

var _ = Describe("Dependency observation contracts", func() {
	It("refuses a changed or missing consumed input", func() {
		item := scripts.Pending{Script: scripts.Script{ID: "example"}, State: scripts.State{Inputs: map[string]string{"result:country": "old", "script:seed": "prerequisite"}}}
		Expect(validateInputs(item, map[string]scripts.Observation{"result:country": {Hash: "old"}})).To(Succeed())
		Expect(validateInputs(item, map[string]scripts.Observation{"result:country": {Hash: "new"}})).NotTo(Succeed())
		Expect(validateInputs(item, map[string]scripts.Observation{})).NotTo(Succeed())
	})
	It("blocks incompatible source history instead of guessing a ledger state", func() {
		db, mock, err := sqlmock.New()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { mock.ExpectClose(); Expect(db.Close()).To(Succeed()) })
		mock.ExpectQuery("SELECT CASE").WillReturnRows(sqlmock.NewRows([]string{"exists", "extended"}).AddRow(1, 1))
		mock.ExpectQuery("SELECT path,hash,state").WithArgs("example").WillReturnRows(sqlmock.NewRows([]string{"path", "hash", "state"}).AddRow("example", []byte{1}, `{"version":2,"hash":"wrong"}`))
		_, err = readStates(context.Background(), db, "example")
		Expect(err).To(MatchError(ContainSubstring("inconsistent ledger")))
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})
	It("does not require table creation to preview an empty ledger", func() {
		db, mock, err := sqlmock.New()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { mock.ExpectClose(); Expect(db.Close()).To(Succeed()) })
		mock.ExpectQuery("SELECT CONVERT").WillReturnRows(sqlmock.NewRows([]string{"server", "database"}).AddRow("example", "disposable"))
		mock.ExpectQuery("SELECT CASE").WillReturnRows(sqlmock.NewRows([]string{"exists", "extended"}).AddRow(0, 0))
		preview, err := Inspect(context.Background(), Options{DB: db, Scope: "example", Scripts: []scripts.Script{{ID: "seed", SQL: "SELECT 1", Phase: scripts.Post}}}, scripts.Post)
		Expect(err).NotTo(HaveOccurred())
		Expect(preview.Plan.Scripts).To(HaveLen(1))
		Expect(preview.Plan.Scripts[0].Selected).To(BeTrue())
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})
	It("samples a shared function once per preview and resamples on the next preview", func(ctx SpecContext) {
		db, mock, err := sqlmock.New()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { mock.ExpectClose(); Expect(db.Close()).To(Succeed()) })
		opts := Options{DB: db, Scope: "epoch", Functions: []Function{{Name: "dbo.country", Type: "nvarchar"}}, Scripts: []scripts.Script{
			{ID: "one", SQL: "SELECT dbo.country()", Phase: scripts.Post},
			{ID: "two", SQL: "SELECT dbo.country()", Phase: scripts.Post},
		}}
		var hashes []string
		for _, country := range []string{"Zimbabwe", "Malawi"} {
			mock.ExpectQuery("SELECT CONVERT").WillReturnRows(sqlmock.NewRows([]string{"server", "database"}).AddRow("example", "disposable"))
			mock.ExpectQuery("SELECT CASE").WillReturnRows(sqlmock.NewRows([]string{"exists", "extended"}).AddRow(0, 0))
			mock.ExpectQuery("SELECT SCHEMA_NAME").WithArgs("[dbo].[country]").WillReturnRows(sqlmock.NewRows([]string{
				"schema", "name", "kind", "definition", "ansi", "quoted", "type", "length", "precision", "scale", "parameters",
			}).AddRow("dbo", "country", "FN", "CREATE FUNCTION dbo.country() RETURNS nvarchar(40) AS BEGIN RETURN N'country' END", true, true, "nvarchar", 80, 0, 0, 0))
			mock.ExpectQuery("SELECT COALESCE").WithArgs("[dbo].[country]").WillReturnRows(sqlmock.NewRows([]string{"server", "db", "schema", "name", "kind", "bound"}))
			mock.ExpectQuery(`SELECT \[dbo\]\.\[country\]\(\)`).WillReturnRows(sqlmock.NewRows([]string{"country"}).AddRow(country))
			preview, err := Inspect(ctx, opts, scripts.Post)
			Expect(err).NotTo(HaveOccurred())
			Expect(preview.Plan.Scripts[0].State.Inputs).To(HaveKey("result:dbo.country"))
			Expect(preview.Plan.Scripts[0].State.Inputs).To(Equal(preview.Plan.Scripts[1].State.Inputs))
			hashes = append(hashes, preview.Plan.Scripts[0].State.Inputs["result:dbo.country"])
		}
		Expect(hashes[0]).NotTo(Equal(hashes[1]))
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})
})
