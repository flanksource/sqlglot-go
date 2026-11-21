//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"errors"
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
		Expect(execSQL(ctx, db, `CREATE FUNCTION dbo.report_country() RETURNS nvarchar(40) AS
BEGIN RETURN dbo.get_country_name(); END`)).To(Succeed())
	})

	It("tracks country result and function definition independently and only reruns the consumer", func(ctx SpecContext) {
		opts := backend.Options{
			DB: db, Scope: "country",
			Functions: []backend.Function{{Name: "dbo.get_country_name", Type: "nvarchar"}},
			Scripts: []scripts.Script{
				{ID: "country", Name: "country", Phase: scripts.Post, SQL: "INSERT dbo.ScriptRuns VALUES (N'country', dbo.get_country_name());"},
				{ID: "report", Name: "report", Phase: scripts.Post, SQL: "INSERT dbo.ScriptRuns VALUES (N'report', dbo.report_country());"},
				{ID: "after-report", Name: "summary", Phase: scripts.Post, SQL: "INSERT dbo.ScriptRuns VALUES (N'after-report', NULL);", DependsOn: []string{"report"}},
				{ID: "other", Name: "other", Phase: scripts.Post, SQL: "INSERT dbo.ScriptRuns VALUES (N'other', NULL);"},
			},
		}
		file := plan(ctx, opts)
		Expect(file.Selected()).To(Equal([]string{"country", "other", "report", "after-report"}))
		var count int
		Expect(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sys.tables WHERE name = N'sqlglot_migrations'").Scan(&count)).To(Succeed())
		Expect(count).To(BeZero())

		Expect(apply(ctx, db, file).Applied).To(Equal([]string{"country", "other", "report", "after-report"}))
		Expect(apply(ctx, db, plan(ctx, opts)).Outcome).To(Equal("already-applied"))
		_, err := backend.Apply(ctx, db, file)
		var stale *backend.StalePlanError
		Expect(errors.As(err, &stale)).To(BeTrue(), "the applied plan is stale once its scripts are recorded")
		Expect(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dbo.ScriptRuns").Scan(&count)).To(Succeed())
		Expect(count).To(Equal(4))

		for _, country := range []string{"Malawi", "Zimbabwe"} {
			Expect(execSQL(ctx, db, "UPDATE dbo.CountryContext SET Name=N'"+country+"'")).To(Succeed())
			file := plan(ctx, opts)
			Expect(file.Selected()).To(Equal([]string{"country", "report", "after-report"}))
			Expect(file.Actions[0].Reasons).To(ContainElement("function-result-changed"))
			Expect(apply(ctx, db, file).Applied).To(Equal([]string{"country", "report", "after-report"}))
		}
		Expect(execSQL(ctx, db, `ALTER FUNCTION dbo.get_country_name() RETURNS nvarchar(40) AS
BEGIN RETURN (SELECT RTRIM(Name) FROM dbo.CountryContext); END`)).To(Succeed())
		file = plan(ctx, opts)
		Expect(file.Selected()).To(Equal([]string{"country", "report", "after-report"}))
		Expect(file.Actions[0].Reasons).To(ContainElement("function-definition-changed"))
		Expect(file.Actions[0].Reasons).NotTo(ContainElement("function-result-changed"))
	})

	It("rolls back a failing batch and never records it as successful", func(ctx SpecContext) {
		opts := backend.Options{DB: db, Scope: "rollback", Scripts: []scripts.Script{{
			ID: "fail", Phase: scripts.Post,
			SQL: "INSERT dbo.ScriptRuns VALUES (N'fail', NULL);\nGO\nTHROW 50001, 'fixture failure', 1;",
		}}}
		result, err := backend.Apply(ctx, db, plan(ctx, opts))
		Expect(err).To(MatchError(ContainSubstring("fixture failure")))
		Expect(result.Outcome).To(Equal("failed"))
		var count int
		Expect(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dbo.ScriptRuns WHERE Script=N'fail'").Scan(&count)).To(Succeed())
		Expect(count).To(BeZero())
		Expect(plan(ctx, opts).Actions[0].Reasons).To(ContainElement("retry-failed"))
	})

	It("keeps committed prerequisites clean while retrying a failed dependency chain", func(ctx SpecContext) {
		Expect(execSQL(ctx, db, "CREATE TABLE dbo.RetryGate (Blocked bit NOT NULL); INSERT dbo.RetryGate VALUES(1)")).To(Succeed())
		opts := backend.Options{DB: db, Scope: "retry-chain", Scripts: []scripts.Script{
			{ID: "first", Phase: scripts.Post, SQL: "INSERT dbo.ScriptRuns VALUES (N'first', NULL);"},
			{ID: "retry", Phase: scripts.Post, DependsOn: []string{"first"}, SQL: `
INSERT dbo.ScriptRuns VALUES (N'retry', NULL);
IF EXISTS(SELECT 1 FROM dbo.RetryGate WHERE Blocked=1) THROW 50001, 'blocked fixture', 1;`},
			{ID: "dependent", Phase: scripts.Post, DependsOn: []string{"retry"}, SQL: "INSERT dbo.ScriptRuns VALUES (N'dependent', NULL);"},
		}}
		failed, err := backend.Apply(ctx, db, plan(ctx, opts))
		Expect(err).To(MatchError(ContainSubstring("blocked fixture")))
		Expect(failed.Applied).To(Equal([]string{"first"}))
		Expect(execSQL(ctx, db, "UPDATE dbo.RetryGate SET Blocked=0")).To(Succeed())
		retry := plan(ctx, opts)
		Expect(retry.Selected()).To(Equal([]string{"retry", "dependent"}))
		Expect(apply(ctx, db, retry).Applied).To(Equal([]string{"retry", "dependent"}))
		var count int
		Expect(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dbo.ScriptRuns WHERE Script=N'first'").Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))
	})

	It("rolls back writes when a script changes a function input it consumed", func(ctx SpecContext) {
		opts := backend.Options{
			DB: db, Scope: "input-drift", Functions: []backend.Function{{Name: "dbo.get_country_name", Type: "nvarchar"}},
			Scripts: []scripts.Script{{ID: "changes-input", Phase: scripts.Post, SQL: `
INSERT dbo.ScriptRuns VALUES(N'changes-input',dbo.get_country_name());
UPDATE dbo.CountryContext SET Name=N'Malawi';`}},
		}
		result, err := backend.Apply(ctx, db, plan(ctx, opts))
		Expect(err).To(MatchError(ContainSubstring("dependency drift")))
		Expect(result.Outcome).To(Equal("failed"))
		var country string
		Expect(db.QueryRowContext(ctx, "SELECT Name FROM dbo.CountryContext").Scan(&country)).To(Succeed())
		Expect(country).To(Equal("Zimbabwe"))
	})

	It("blocks missing required scalar results", func(ctx SpecContext) {
		Expect(execSQL(ctx, db, "CREATE TABLE dbo.NullableContext (Label nvarchar(40) NULL); INSERT dbo.NullableContext VALUES(NULL)")).To(Succeed())
		Expect(execSQL(ctx, db, `CREATE FUNCTION dbo.get_required_context() RETURNS nvarchar(40) AS
BEGIN RETURN (SELECT Label FROM dbo.NullableContext); END`)).To(Succeed())
		opts := backend.Options{DB: db, Scope: "required-context",
			Functions: []backend.Function{{Name: "dbo.get_required_context", Type: "nvarchar"}},
			Scripts:   []scripts.Script{{ID: "consumer", Phase: scripts.Post, SQL: "SELECT dbo.get_required_context();"}},
		}
		for _, value := range []string{"NULL", "N''"} {
			Expect(execSQL(ctx, db, "UPDATE dbo.NullableContext SET Label="+value)).To(Succeed())
			file := plan(ctx, opts)
			Expect(file.Selected()).To(BeEmpty())
			Expect(file.Actions[0].Blocked).To(ConsistOf(ContainSubstring("NULL or an empty context")))
		}
	})

	It("blocks unknown SQL dispatch, missing definitions, unsupported probes and cycles", func(ctx SpecContext) {
		Expect(execSQL(ctx, db, "CREATE FUNCTION dbo.unstable_context() RETURNS nvarchar(40) AS BEGIN RETURN CONVERT(nvarchar(40),GETDATE()); END")).To(Succeed())
		Expect(execSQL(ctx, db, "CREATE FUNCTION dbo.unsupported_context() RETURNS int AS BEGIN RETURN 1; END")).To(Succeed())
		opts := backend.Options{
			DB: db, Scope: "diagnostics",
			Functions: []backend.Function{{Name: "dbo.unstable_context", Type: "nvarchar"}, {Name: "dbo.unsupported_context", Type: "int"}},
			Scripts: []scripts.Script{
				{ID: "dynamic", Phase: scripts.Post, SQL: "DECLARE @query nvarchar(max) = N'SELECT 1'; EXEC(@query);"},
				{ID: "missing", Phase: scripts.Post, SQL: "SELECT dbo.missing_context()"},
				{ID: "unsupported", Phase: scripts.Post, SQL: "SELECT dbo.unsupported_context()"},
				{ID: "unstable", Phase: scripts.Post, SQL: "SELECT dbo.unstable_context()"},
			},
		}
		file := plan(ctx, opts)
		Expect(file.Selected()).To(BeEmpty())
		wanted := map[string]string{"dynamic": "explicit dependencies", "missing": "not found or not visible", "unsupported": "unsupported", "unstable": "unstable builtin"}
		for _, item := range file.Actions {
			Expect(item.Blocked).To(ContainElement(ContainSubstring(wanted[item.Script.ID])))
		}
		opts.Scripts[0].DependsOn = []string{"missing"}
		opts.Scripts[1].DependsOn = []string{"dynamic"}
		_, err := backend.Plan(ctx, opts)
		Expect(err).To(MatchError(ContainSubstring("dependency cycle")))
	})

	It("refuses a plan whose input changed, or whose file was edited, without running it", func(ctx SpecContext) {
		opts := backend.Options{
			DB: db, Scope: "review-binding",
			Functions: []backend.Function{{Name: "dbo.get_country_name", Type: "nvarchar"}},
			Scripts:   []scripts.Script{{ID: "bound", Phase: scripts.Post, SQL: "INSERT dbo.ScriptRuns VALUES(N'bound',dbo.get_country_name());"}},
		}
		file := plan(ctx, opts)
		edited := file
		edited.Bundle.Scripts = []scripts.Script{{ID: "bound", Phase: scripts.Post, SQL: "INSERT dbo.ScriptRuns VALUES(N'bound',N'changed');"}}
		_, err := backend.Apply(ctx, db, edited)
		Expect(err).To(MatchError(ContainSubstring("integrity check failed")))
		Expect(execSQL(ctx, db, "UPDATE dbo.CountryContext SET Name=N'Malawi'")).To(Succeed())
		result, err := backend.Apply(ctx, db, file)
		Expect(err).To(MatchError(ContainSubstring("observed input result:dbo.get_country_name changed")))
		Expect(result.Outcome).To(Equal("stale"))
		var count int
		Expect(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dbo.ScriptRuns WHERE Script=N'bound'").Scan(&count)).To(Succeed())
		Expect(count).To(BeZero())
	})
})

func plan(ctx context.Context, opts backend.Options) backend.PlanFile {
	GinkgoHelper()
	file, err := backend.Plan(ctx, opts)
	Expect(err).NotTo(HaveOccurred())
	return file
}

func apply(ctx context.Context, db *sql.DB, file backend.PlanFile) backend.Result {
	GinkgoHelper()
	result, err := backend.Apply(ctx, db, file)
	Expect(err).NotTo(HaveOccurred())
	return result
}

func execSQL(ctx context.Context, db *sql.DB, statement string) error {
	_, err := db.ExecContext(ctx, statement)
	return err
}
