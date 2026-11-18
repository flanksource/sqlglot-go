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
		preview, err := backend.Inspect(ctx, opts, scripts.Post)
		Expect(err).NotTo(HaveOccurred())
		Expect(selectedIDs(preview)).To(Equal([]string{"country", "other", "report", "after-report"}))
		var count int
		Expect(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sys.tables WHERE name = N'sqlglot_migrations'").Scan(&count)).To(Succeed())
		Expect(count).To(BeZero())

		session, err := backend.Open(ctx, opts)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(session.Close)
		Expect(apply(ctx, session, preview).Applied).To(Equal([]string{"country", "other", "report", "after-report"}))
		Expect(apply(ctx, session, preview).Outcome).To(Equal("already-applied"))
		Expect(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dbo.ScriptRuns").Scan(&count)).To(Succeed())
		Expect(count).To(Equal(4))
		AddReportEntry("C01", map[string]any{"order": []string{"country", "other", "report", "after-report"}, "rowsAfterSecondApply": count})

		for _, country := range []string{"Malawi", "Zimbabwe"} {
			_, err := db.ExecContext(ctx, "UPDATE dbo.CountryContext SET Name=@p1", country)
			Expect(err).NotTo(HaveOccurred())
			preview, err := session.Inspect(ctx, scripts.Post)
			Expect(err).NotTo(HaveOccurred())
			Expect(selectedIDs(preview)).To(Equal([]string{"country", "report", "after-report"}))
			Expect(preview.Plan.Scripts[0].Reasons).To(ContainElement("function-result-changed"))
			Expect(apply(ctx, session, preview).Applied).To(Equal([]string{"country", "report", "after-report"}))
			AddReportEntry("C04", map[string]any{"country": country, "selected": selectedIDs(preview)})
		}
		Expect(execSQL(ctx, db, `ALTER FUNCTION dbo.get_country_name() RETURNS nvarchar(40) AS
BEGIN RETURN (SELECT RTRIM(Name) FROM dbo.CountryContext); END`)).To(Succeed())
		preview, err = session.Inspect(ctx, scripts.Post)
		Expect(err).NotTo(HaveOccurred())
		Expect(selectedIDs(preview)).To(Equal([]string{"country", "report", "after-report"}))
		Expect(preview.Plan.Scripts[0].Reasons).To(ContainElement("function-definition-changed"))
		Expect(preview.Plan.Scripts[0].Reasons).NotTo(ContainElement("function-result-changed"))
		AddReportEntry("C05", map[string]any{"selected": selectedIDs(preview), "reasons": preview.Plan.Scripts[0].Reasons})
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

	It("keeps committed prerequisites clean while retrying a failed dependency chain", func(ctx SpecContext) {
		Expect(execSQL(ctx, db, "CREATE TABLE dbo.RetryGate (Blocked bit NOT NULL); INSERT dbo.RetryGate VALUES(1)")).To(Succeed())
		inventory := []scripts.Script{
			{ID: "first", Phase: scripts.Post, SQL: "INSERT dbo.ScriptRuns VALUES (N'first', NULL);"},
			{ID: "retry", Phase: scripts.Post, DependsOn: []string{"first"}, SQL: `
INSERT dbo.ScriptRuns VALUES (N'retry', NULL);
IF EXISTS(SELECT 1 FROM dbo.RetryGate WHERE Blocked=1) THROW 50001, 'blocked fixture', 1;`},
			{ID: "dependent", Phase: scripts.Post, DependsOn: []string{"retry"}, SQL: "INSERT dbo.ScriptRuns VALUES (N'dependent', NULL);"},
		}
		session, err := backend.Open(ctx, backend.Options{DB: db, Scope: "retry-chain", Scripts: inventory})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(session.Close)
		initial, err := session.Inspect(ctx, scripts.Post)
		Expect(err).NotTo(HaveOccurred())
		failed, err := session.Apply(ctx, backend.ApplyOptions{Preview: initial})
		Expect(err).To(MatchError(ContainSubstring("blocked fixture")))
		Expect(failed.Applied).To(Equal([]string{"first"}))
		var count int
		Expect(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dbo.ScriptRuns WHERE Script IN (N'first',N'retry',N'dependent')").Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))
		Expect(execSQL(ctx, db, "UPDATE dbo.RetryGate SET Blocked=0")).To(Succeed())
		retry, err := session.Inspect(ctx, scripts.Post)
		Expect(err).NotTo(HaveOccurred())
		Expect(selectedIDs(retry)).To(Equal([]string{"retry", "dependent"}))
		Expect(apply(ctx, session, retry).Applied).To(Equal([]string{"retry", "dependent"}))
		Expect(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dbo.ScriptRuns WHERE Script=N'first'").Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))
		AddReportEntry("C07", map[string]any{"committedBeforeFailure": failed.Applied, "retrySelection": selectedIDs(retry), "prerequisiteExecutions": count})
	})

	It("rolls back writes when a script changes a function input it consumed", func(ctx SpecContext) {
		session, err := backend.Open(ctx, backend.Options{
			DB: db, Scope: "input-drift", Functions: []backend.Function{{Name: "dbo.get_country_name", Type: "nvarchar"}},
			Scripts: []scripts.Script{{ID: "changes-input", Phase: scripts.Post, SQL: `
INSERT dbo.ScriptRuns VALUES(N'changes-input',dbo.get_country_name());
UPDATE dbo.CountryContext SET Name=N'Malawi';`}},
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(session.Close)
		preview, err := session.Inspect(ctx, scripts.Post)
		Expect(err).NotTo(HaveOccurred())
		result, err := session.Apply(ctx, backend.ApplyOptions{Preview: preview})
		Expect(err).To(MatchError(ContainSubstring("dependency drift")))
		Expect(result.Outcome).To(Equal("failed"))
		var country string
		Expect(db.QueryRowContext(ctx, "SELECT Name FROM dbo.CountryContext").Scan(&country)).To(Succeed())
		Expect(country).To(Equal("Zimbabwe"))
		var count int
		Expect(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dbo.ScriptRuns WHERE Script=N'changes-input'").Scan(&count)).To(Succeed())
		Expect(count).To(BeZero())
	})

	It("blocks missing required scalar results", func(ctx SpecContext) {
		Expect(execSQL(ctx, db, "CREATE TABLE dbo.NullableContext (Label nvarchar(40) NULL); INSERT dbo.NullableContext VALUES(NULL)")).To(Succeed())
		Expect(execSQL(ctx, db, `CREATE FUNCTION dbo.get_required_context() RETURNS nvarchar(40) AS
BEGIN RETURN (SELECT Label FROM dbo.NullableContext); END`)).To(Succeed())
		opts := backend.Options{DB: db, Scope: "required-context",
			Functions: []backend.Function{{Name: "dbo.get_required_context", Type: "nvarchar"}},
			Scripts:   []scripts.Script{{ID: "consumer", Phase: scripts.Post, SQL: "SELECT dbo.get_required_context();"}},
		}
		var blocked [][]string
		for _, value := range []any{nil, ""} {
			_, err := db.ExecContext(ctx, "UPDATE dbo.NullableContext SET Label=@p1", value)
			Expect(err).NotTo(HaveOccurred())
			plan, err := backend.Inspect(ctx, opts, scripts.Post)
			Expect(err).NotTo(HaveOccurred())
			Expect(selectedIDs(plan)).To(BeEmpty())
			Expect(plan.Plan.Scripts[0].Blocked).To(ConsistOf(ContainSubstring("NULL or an empty context")))
			blocked = append(blocked, plan.Plan.Scripts[0].Blocked)
		}
		AddReportEntry("required-scalar", blocked)
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
		plan, err := backend.Inspect(ctx, opts, scripts.Post)
		Expect(err).NotTo(HaveOccurred())
		Expect(selectedIDs(plan)).To(BeEmpty())
		wanted := map[string]string{"dynamic": "explicit dependencies", "missing": "not found or not visible", "unsupported": "unsupported", "unstable": "unstable builtin"}
		diagnostics := make(map[string][]string, len(wanted))
		for _, item := range plan.Plan.Scripts {
			Expect(item.Blocked).To(ContainElement(ContainSubstring(wanted[item.Script.ID])))
			diagnostics[item.Script.ID] = item.Blocked
		}
		opts.Scripts[0].DependsOn = []string{"missing"}
		opts.Scripts[1].DependsOn = []string{"dynamic"}
		_, err = backend.Inspect(ctx, opts, scripts.Post)
		Expect(err).To(MatchError(ContainSubstring("dependency cycle")))
		diagnostics["cycle"] = []string{err.Error()}
		AddReportEntry("C18", diagnostics)
	})

	It("refuses an approved plan after source, registry or inputs change", func(ctx SpecContext) {
		options := backend.Options{
			DB: db, Scope: "review-binding",
			Functions: []backend.Function{{Name: "dbo.get_country_name", Type: "nvarchar"}},
			Scripts:   []scripts.Script{{ID: "bound", Phase: scripts.Post, SQL: "INSERT dbo.ScriptRuns VALUES(N'bound',dbo.get_country_name());"}},
		}
		approved, err := backend.Inspect(ctx, options, scripts.Post)
		Expect(err).NotTo(HaveOccurred())
		Expect(selectedIDs(approved)).To(Equal([]string{"bound"}))
		var binds = map[string]backend.Options{}
		source := options
		source.Scripts = []scripts.Script{{ID: "bound", Phase: scripts.Post, SQL: "INSERT dbo.ScriptRuns VALUES(N'bound',N'changed');"}}
		binds["source"] = source
		registry := options
		registry.Functions = []backend.Function{{Name: "dbo.get_country_name", Type: "nvarchar", Nullable: true}}
		binds["registry"] = registry
		binds["input"] = options
		outcomes := map[string]string{}
		for _, change := range []string{"source", "registry", "input"} {
			if change == "input" {
				Expect(execSQL(ctx, db, "UPDATE dbo.CountryContext SET Name=N'Malawi'")).To(Succeed())
			}
			session, err := backend.Open(ctx, binds[change])
			Expect(err).NotTo(HaveOccurred())
			outcome := apply(ctx, session, approved)
			Expect(session.Close()).To(Succeed())
			Expect(outcome.Applied).To(BeEmpty())
			Expect(outcome.Continuation).NotTo(BeNil())
			Expect(outcome.Outcome).To(Equal("continuation-required"))
			outcomes[change] = outcome.Outcome
		}
		var count int
		Expect(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dbo.ScriptRuns WHERE Script=N'bound'").Scan(&count)).To(Succeed())
		Expect(count).To(BeZero())
		AddReportEntry("review-binding", outcomes)
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
