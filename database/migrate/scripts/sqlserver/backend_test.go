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
		_, err = readStates(context.Background(), db, ledgerScope{Scope: "example"})
		Expect(err).To(MatchError(ContainSubstring("inconsistent ledger")))
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})
	It("does not require table creation to preview an empty ledger", func() {
		db, mock, err := sqlmock.New()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { mock.ExpectClose(); Expect(db.Close()).To(Succeed()) })
		mock.ExpectQuery("SELECT CONVERT").WillReturnRows(sqlmock.NewRows([]string{"server", "database"}).AddRow("example", "disposable"))
		mock.ExpectQuery("SELECT CASE").WillReturnRows(sqlmock.NewRows([]string{"exists", "extended"}).AddRow(0, 0))
		file, err := Plan(context.Background(), Options{DB: db, Scope: "example", Scripts: []scripts.Script{{ID: "seed", SQL: "SELECT 1", Phase: scripts.Post}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(file.Actions).To(HaveLen(1))
		Expect(file.Actions[0].Selected).To(BeTrue())
		Expect(file.Target).To(Equal(PlanTarget{Server: "example", Database: "disposable", Scope: "example"}))
		Expect(file.Verify()).To(Succeed())
		previous := file
		previous.Version = 1
		Expect(previous.Verify()).To(MatchError(ContainSubstring("re-run sqlglot migrate plan")))
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
			expectTarget(mock)
			expectLevel(mock, []string{"dbo.country"}, function("dbo.country", "N'country'"))
			mock.ExpectQuery(`SELECT \[dbo\]\.\[country\]\(\)`).WillReturnRows(sqlmock.NewRows([]string{"country"}).AddRow(country))
			file, err := Plan(ctx, opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(file.Actions[0].State.Inputs).To(HaveKey("result:dbo.country"))
			Expect(file.Actions[0].State.Inputs).To(Equal(file.Actions[1].State.Inputs))
			hashes = append(hashes, file.Actions[0].State.Inputs["result:dbo.country"])
		}
		Expect(hashes[0]).NotTo(Equal(hashes[1]))
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})
	It("plans several phases from one ledger read and one observation epoch", func(ctx SpecContext) {
		db, mock, err := sqlmock.New()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { mock.ExpectClose(); Expect(db.Close()).To(Succeed()) })
		opts := Options{DB: db, Scope: "phases", Functions: []Function{{Name: "dbo.country", Type: "nvarchar"}}, Scripts: []scripts.Script{
			{ID: "seed", SQL: "SELECT dbo.country()", Phase: scripts.Pre},
			{ID: "report", SQL: "SELECT dbo.country()", Phase: scripts.Post},
		}}
		expectTarget(mock)
		expectLevel(mock, []string{"dbo.country"}, function("dbo.country", "N'country'"))
		mock.ExpectQuery(`SELECT \[dbo\]\.\[country\]\(\)`).WillReturnRows(sqlmock.NewRows([]string{"country"}).AddRow("Zimbabwe"))

		file, err := Plan(ctx, opts)

		Expect(err).NotTo(HaveOccurred())
		selected := map[scripts.Phase][]string{}
		for _, item := range file.Actions {
			if item.Selected {
				selected[item.Script.Phase] = append(selected[item.Script.Phase], item.Script.ID)
			}
		}
		Expect(selected).To(Equal(map[scripts.Phase][]string{scripts.Pre: {"seed"}, scripts.Post: {"report"}}))
		Expect(file.Observations).To(Equal(map[string]string{
			"result:dbo.country": file.Actions[0].State.Inputs["result:dbo.country"],
		}))
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})
})
