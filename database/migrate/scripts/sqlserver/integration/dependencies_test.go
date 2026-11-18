//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	backend "github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver/sqlservertest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestScriptDependenciesIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Disposable SQL Server script dependencies")
}

var _ = Describe("Migration dependencies on a disposable SQL Server", Ordered, func() {
	var db *sql.DB
	BeforeAll(func(ctx SpecContext) {
		db = sqlservertest.OpenDisposable(ctx)
		Expect(execSQL(ctx, db, `CREATE TABLE dbo.CountryContext (Name nvarchar(40) NOT NULL);
INSERT dbo.CountryContext VALUES (N'Zimbabwe');
CREATE TABLE dbo.ScriptRuns (Script nvarchar(40) NOT NULL, Observed nvarchar(40) NULL);`)).To(Succeed())
		Expect(execSQL(ctx, db, `CREATE FUNCTION dbo.get_country_name() RETURNS nvarchar(40) AS
BEGIN RETURN (SELECT Name FROM dbo.CountryContext); END`)).To(Succeed())
	})

	It("tracks country result and function definition independently and only reruns the consumer", func(ctx SpecContext) {
		opts := backend.Options{
			DB: db, Scope: "country",
			Functions: []backend.Function{{Name: "dbo.get_country_name", Type: "nvarchar"}},
			Scripts: []scripts.Script{
				{ID: "country", Name: "country", Phase: scripts.Post, SQL: "INSERT dbo.ScriptRuns VALUES (N'country', dbo.get_country_name());"},
				{ID: "other", Name: "other", Phase: scripts.Post, SQL: "INSERT dbo.ScriptRuns VALUES (N'other', NULL);"},
			},
		}
		preview, err := backend.Inspect(ctx, opts, scripts.Post)
		Expect(err).NotTo(HaveOccurred())
		Expect(selectedIDs(preview)).To(Equal([]string{"country", "other"}))
		var count int
		Expect(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sys.tables WHERE name = N'sqlglot_migrations'").Scan(&count)).To(Succeed())
		Expect(count).To(BeZero())

		session, err := backend.Open(ctx, opts)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(session.Close)
		Expect(apply(ctx, session, preview).Applied).To(Equal([]string{"country", "other"}))
		Expect(apply(ctx, session, preview).Outcome).To(Equal("already-applied"))

		for _, country := range []string{"Malawi", "Zimbabwe"} {
			_, err := db.ExecContext(ctx, "UPDATE dbo.CountryContext SET Name=@p1", country)
			Expect(err).NotTo(HaveOccurred())
			preview, err := session.Inspect(ctx, scripts.Post)
			Expect(err).NotTo(HaveOccurred())
			Expect(selectedIDs(preview)).To(Equal([]string{"country"}))
			Expect(preview.Plan.Scripts[0].Reasons).To(ContainElement("function-result-changed"))
			Expect(apply(ctx, session, preview).Applied).To(Equal([]string{"country"}))
		}
		Expect(execSQL(ctx, db, `ALTER FUNCTION dbo.get_country_name() RETURNS nvarchar(40) AS
BEGIN RETURN (SELECT RTRIM(Name) FROM dbo.CountryContext); END`)).To(Succeed())
		preview, err = session.Inspect(ctx, scripts.Post)
		Expect(err).NotTo(HaveOccurred())
		Expect(selectedIDs(preview)).To(Equal([]string{"country"}))
		Expect(preview.Plan.Scripts[0].Reasons).To(ContainElement("function-definition-changed"))
		Expect(preview.Plan.Scripts[0].Reasons).NotTo(ContainElement("function-result-changed"))
	})

	It("rolls back a failing batch and never records it as successful", func(ctx SpecContext) {
		session, err := backend.Open(ctx, backend.Options{DB: db, Scope: "rollback", Scripts: []scripts.Script{{
			ID: "fail", Phase: scripts.Post,
			SQL: "INSERT dbo.ScriptRuns VALUES (N'fail', NULL);\nGO\nTHROW 50001, 'fixture failure', 1;",
		}}})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(session.Close)
		preview, err := session.Inspect(ctx, scripts.Post)
		Expect(err).NotTo(HaveOccurred())
		result, err := session.Apply(ctx, backend.ApplyOptions{Preview: preview})
		Expect(err).To(MatchError(ContainSubstring("fixture failure")))
		Expect(result.Outcome).To(Equal("failed"))
		var count int
		Expect(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dbo.ScriptRuns WHERE Script=N'fail'").Scan(&count)).To(Succeed())
		Expect(count).To(BeZero())
		preview, err = session.Inspect(ctx, scripts.Post)
		Expect(err).NotTo(HaveOccurred())
		Expect(preview.Plan.Scripts[0].Reasons).To(ContainElement("retry-failed"))
	})
})

func selectedIDs(preview backend.Preview) []string {
	var ids []string
	for _, item := range preview.Plan.Scripts {
		if item.Selected {
			ids = append(ids, item.Script.ID)
		}
	}
	return ids
}

func apply(ctx context.Context, session *backend.Session, preview backend.Preview) backend.Result {
	result, err := session.Apply(ctx, backend.ApplyOptions{Preview: preview})
	Expect(err).NotTo(HaveOccurred())
	return result
}

func execSQL(ctx context.Context, db *sql.DB, statement string) error {
	_, err := db.ExecContext(ctx, statement)
	return err
}
