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

	It("reruns the consumers of a changed country result, or of a changed function with no sampled result", func(ctx SpecContext) {
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
			previous := map[string]string{"Malawi": "Zimbabwe", "Zimbabwe": "Malawi"}[country]
			Expect(file.Functions()[0]).To(Equal(scripts.FunctionInput{Name: "dbo.get_country_name", Sampled: true, Value: &country, Uses: []scripts.FunctionUse{
				{Script: "country", Recorded: true, Previous: &previous, ValueChanged: true}, {Script: "report", Recorded: true, Previous: &previous, ValueChanged: true}}}))
			Expect(apply(ctx, db, file).Applied).To(Equal([]string{"country", "report", "after-report"}))
		}
		Expect(execSQL(ctx, db, `ALTER FUNCTION dbo.get_country_name() RETURNS nvarchar(40) AS
BEGIN RETURN (SELECT RTRIM(Name) FROM dbo.CountryContext); END`)).To(Succeed())
		Expect(plan(ctx, opts).Selected()).To(BeEmpty(), "the edited function still returns the same country")
		Expect(execSQL(ctx, db, `ALTER FUNCTION dbo.report_country() RETURNS nvarchar(40) AS
BEGIN RETURN UPPER(dbo.get_country_name()); END`)).To(Succeed())
		file = plan(ctx, opts)
		Expect(file.Selected()).To(Equal([]string{"report", "after-report"}), "a function with no sampled result is tracked by its definition")
		for _, item := range file.Actions {
			if item.Script.ID == "report" {
				Expect(item.Reasons).To(Equal([]string{"function-definition-changed"}))
			}
		}
	})

	It("re-runs a reader in the plan after a write that changed what it reads", func(ctx SpecContext) {
		Expect(execSQL(ctx, db, `CREATE TABLE dbo.ReadCodes (CodeName varchar(20), CodeValue varchar(20));
CREATE TABLE dbo.ReadMap (Name varchar(20), Value varchar(20));
INSERT dbo.ReadCodes VALUES ('Title','Mr'), ('Cause','Fire')`)).To(Succeed())
		Expect(execSQL(ctx, db, "CREATE PROCEDURE dbo.map_codes AS INSERT dbo.ReadMap SELECT CodeName, CodeValue FROM dbo.ReadCodes WHERE CodeName='Cause'")).To(Succeed())
		source := "UPDATE dbo.ReadCodes SET CodeValue='Mr' WHERE CodeName='Title'"
		opts := func() backend.Options {
			return backend.Options{DB: db, Scope: "reads", Scripts: []scripts.Script{
				{ID: "a-write", Phase: scripts.Pre, SQL: source},
				{ID: "b-title", Phase: scripts.Post, SQL: "DECLARE @n int\nDECLARE @code varchar(20) = 'Title'\nSELECT @n = COUNT(*) FROM dbo.ReadCodes WHERE CodeName = @code AND CodeValue = 'Mr'\nIF @n = 1\nINSERT dbo.ReadMap VALUES ('title', 'Mr')"},
				{ID: "c-cause", Phase: scripts.Post, SQL: "DELETE FROM dbo.ReadMap WHERE Name='cause'\nINSERT dbo.ReadMap SELECT 'cause', CodeValue FROM dbo.ReadCodes WHERE CodeName='Cause'"},
				{ID: "d-map", Phase: scripts.Post, SQL: "INSERT dbo.ScriptRuns SELECT N'd-map', Value FROM dbo.ReadMap WHERE Name='cause'"},
				{ID: "e-routine", Phase: scripts.Post, SQL: "EXEC dbo.map_codes"},
			}}
		}
		first := apply(ctx, db, plan(ctx, opts()))
		Expect(first.Applied).To(Equal([]string{"a-write", "b-title", "c-cause", "d-map", "e-routine"}))
		settled := plan(ctx, opts())
		Expect(settled.Selected()).To(Equal([]string{"d-map"}), "e-routine, a later script, wrote a cause to the map d-map reads")
		Expect(apply(ctx, db, settled).Applied).To(Equal([]string{"d-map"}))
		Expect(plan(ctx, opts()).Selected()).To(BeEmpty())
		routine := plan(ctx, opts()).Actions[4]
		Expect([]any{routine.Reads, routine.UntrackedReads}).To(Equal([]any{[]scripts.PlannedRead(nil), []scripts.ObjectName{scripts.NewObjectName("dbo", "ReadCodes")}}))

		source += "\nUPDATE dbo.ReadCodes SET CodeValue='Dr' WHERE CodeName='Title'"
		Expect(apply(ctx, db, plan(ctx, opts())).Applied).To(Equal([]string{"a-write"}))
		title := plan(ctx, opts())
		Expect(title.Selected()).To(Equal([]string{"b-title"}))
		read := title.Actions[1].Reads[0]
		Expect(title.Actions[1].Reasons).To(Equal([]string{scripts.ReadChanged}))
		Expect([]any{read.SQL, *read.Recorded.Value, *read.Answer.Value}).To(Equal([]any{
			"DECLARE @CODE VARCHAR(20) = 'Title';\nSELECT COUNT(*) FROM dbo.ReadCodes WHERE CodeName = @code AND CodeValue = 'Mr'", "1", "0"}))
		apply(ctx, db, title)

		Expect(execSQL(ctx, db, "UPDATE dbo.ReadCodes SET CodeValue='Flood' WHERE CodeName='Cause'")).To(Succeed())
		cause := plan(ctx, opts())
		Expect(cause.Selected()).To(Equal([]string{"c-cause"}))
		Expect(apply(ctx, db, cause).Applied).To(Equal([]string{"c-cause"}), "its own writes do not select it again")
		Expect(plan(ctx, opts()).Selected()).To(Equal([]string{"d-map"}))

		Expect(execSQL(ctx, db, "EXEC sp_rename 'dbo.ReadCodes', 'ReadCodesGone'")).To(Succeed())
		gone := plan(ctx, opts()).Actions[1]
		Expect(gone.Selected).To(BeFalse())
		Expect(gone.Reads[0].Answer.Error).To(ContainSubstring("Invalid object name 'dbo.ReadCodes'"))
	})

	It("tracks the value of an environment call, not the database name inside it", func(ctx SpecContext) {
		opts := backend.Options{DB: db, Scope: "environment", Scripts: []scripts.Script{
			{ID: "prefix", Phase: scripts.Post, SQL: "IF (SELECT SUBSTRING((SELECT DB_NAME()),1,6)) = N'script' INSERT dbo.ScriptRuns VALUES (N'prefix', NULL);"},
			{ID: "name", Phase: scripts.Post, SQL: "INSERT dbo.ScriptRuns VALUES (N'name', LEFT(DB_NAME(), 40));"},
		}}
		Expect(apply(ctx, db, plan(ctx, opts)).Applied).To(Equal([]string{"name", "prefix"}))
		// As if the runs were recorded in script_renamed, this database's earlier name.
		Expect(execSQL(ctx, db, `UPDATE dbo.sqlglot_migrations SET state=REPLACE(state,N'script_dependencies',N'script_renamed') WHERE scope=N'environment'`)).To(Succeed())
		file := plan(ctx, opts)
		Expect(file.Selected()).To(Equal([]string{"name"}))
		value, previous := "script_dependencies", "script_renamed"
		Expect(file.Functions()).To(ContainElement(scripts.FunctionInput{Name: "LEFT(DB_NAME(), 40)", Builtin: true, Sampled: true, Value: &value,
			Uses: []scripts.FunctionUse{{Script: "name", Recorded: true, Previous: &previous, ValueChanged: true}}}))
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

	DescribeTable("runs a script that controls its own transaction inside the migration's",
		func(ctx SpecContext, id, sql, outcome, problem string, rows int) {
			after := id + "-after"
			opts := backend.Options{DB: db, Scope: id, Scripts: []scripts.Script{{ID: id, Name: id, Phase: scripts.Post, SQL: sql},
				{ID: after, Name: after, Phase: scripts.Post, SQL: "INSERT dbo.ScriptRuns VALUES (N'" + after + "', NULL);"}}}
			file := plan(ctx, opts)
			Expect(file.Selected()).To(Equal([]string{id, after}))
			Expect(file.Warnings).To(ContainElement(ContainSubstring("controls its own transaction")))
			result, err := backend.Apply(ctx, db, file)
			if problem == "" {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(MatchError(ContainSubstring(problem)))
			}
			var count int
			Expect(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dbo.ScriptRuns WHERE Script=@p1", id).Scan(&count)).To(Succeed())
			var recorded string
			Expect(db.QueryRowContext(ctx, "SELECT JSON_VALUE(state,'$.outcome') FROM dbo.sqlglot_migrations WHERE scope=@p1 AND path=@p1", id).Scan(&recorded)).To(Succeed())
			Expect([]any{count, recorded, result.Applied}).To(Equal([]any{rows, outcome, map[bool][]string{true: {id, after}, false: {after}}[problem == ""]}))
			Expect(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dbo.ScriptRuns WHERE Script=@p1", after).Scan(&count)).To(Succeed())
			Expect(count).To(Equal(1), "the next script commits")
		},
		Entry("committing it", "own-commit", "BEGIN TRANSACTION\nINSERT dbo.ScriptRuns VALUES (N'own-commit', NULL);\nCOMMIT", "applied", "", 1),
		Entry("committing it in a later batch", "own-batches", "BEGIN TRANSACTION\nGO\nINSERT dbo.ScriptRuns VALUES (N'own-batches', NULL);\nGO\nCOMMIT", "applied", "", 1),
		Entry("failing after its own commit", "own-failure", "BEGIN TRANSACTION\nINSERT dbo.ScriptRuns VALUES (N'own-failure', NULL);\nCOMMIT\nGO\nTHROW 50001, 'own fixture failure', 1;", "failed", "own fixture failure", 0),
		Entry("leaving it open", "own-open", "BEGIN TRANSACTION\nINSERT dbo.ScriptRuns VALUES (N'own-open', NULL);", "commit-unknown", "transaction count to 2", 0),
		Entry("rolling it back", "own-rollback", "BEGIN TRANSACTION\nINSERT dbo.ScriptRuns VALUES (N'own-rollback', NULL);\nROLLBACK", "commit-unknown", "does not have an active transaction", 0),
		Entry("rolling it back in its own CATCH", "own-catch", "BEGIN TRY\nBEGIN TRANSACTION;\nINSERT dbo.ScriptRuns VALUES (N'own-catch', NULL);\nTHROW 50001, 'catch fixture', 1;\nEND TRY\nBEGIN CATCH\nROLLBACK;\nTHROW;\nEND CATCH", "failed", "catch fixture", 0),
		Entry("aborting it with XACT_ABORT", "own-abort", "SET XACT_ABORT ON;\nBEGIN TRANSACTION;\nINSERT dbo.ScriptRuns VALUES (N'own-abort', NULL);\nTHROW 50001, 'abort fixture', 1;", "failed", "abort fixture", 0),
	)

	It("runs past failed scripts, skipping only their direct dependents, and retries both", func(ctx SpecContext) {
		Expect(execSQL(ctx, db, "CREATE TABLE dbo.Codes (Code nvarchar(10) PRIMARY KEY); INSERT dbo.Codes VALUES (N'MF345')")).To(Succeed())
		run := func(id string) string { return "INSERT dbo.ScriptRuns VALUES (N'" + id + "', NULL);" }
		opts := backend.Options{DB: db, Scope: "past-failures", Scripts: []scripts.Script{
			{ID: "codes", Phase: scripts.Pre, SQL: run("codes") + "\nGO\nINSERT dbo.Codes VALUES (N'MF345');"},
			{ID: "codes-dependent", Phase: scripts.Pre, SQL: run("codes-dependent"), DependsOn: []string{"codes"}},
			{ID: "codes-reader", Phase: scripts.Post, SQL: "INSERT dbo.ScriptRuns SELECT N'codes-reader', MAX(Code) FROM dbo.Codes;"},
			{ID: "define", Phase: scripts.Pre, SQL: "CREATE PROCEDURE dbo.past_failure AS SELECT 1;\nGO\nTHROW 50001, 'define fixture', 1;"},
			{ID: "define-caller", Phase: scripts.Post, SQL: "EXEC dbo.past_failure;"},
			{ID: "untransacted", Phase: scripts.Post, NonTransactional: true, SQL: "BEGIN TRANSACTION;\n" + run("untransacted") + "\nTHROW 50001, 'partial fixture', 1;"},
			{ID: "zz-last", Phase: scripts.Post, SQL: run("zz-last")},
		}}
		result, err := backend.Apply(ctx, db, plan(ctx, opts))
		Expect(err).To(MatchError(SatisfyAll(ContainSubstring("Violation of PRIMARY KEY"), ContainSubstring("define fixture"), ContainSubstring("partial fixture"))))
		failures := map[string]string{}
		for _, failure := range result.Failed {
			failures[failure.Script] = failure.Outcome
		}
		Expect(failures).To(Equal(map[string]string{"codes": "failed", "define": "failed", "untransacted": "partial"}))
		Expect(result.Outcome).To(Equal("partial"))
		Expect(result.Applied).To(ConsistOf("codes-reader", "zz-last"))
		Expect(result.Skipped).To(ConsistOf(backend.SkippedScript{Script: "codes-dependent", Dependency: "codes"}, backend.SkippedScript{Script: "define-caller", Dependency: "define"}))
		var ran string
		Expect(db.QueryRowContext(ctx, `SELECT STRING_AGG(Script, ',') WITHIN GROUP (ORDER BY Script) FROM dbo.ScriptRuns
WHERE Script IN (N'codes',N'codes-dependent',N'codes-reader',N'untransacted',N'zz-last')`).Scan(&ran)).To(Succeed())
		Expect(ran).To(Equal("codes-reader,zz-last"), "a failed script's own writes are rolled back")

		Expect(execSQL(ctx, db, "DELETE dbo.Codes")).To(Succeed())
		retry := plan(ctx, opts)
		Expect(retry.Selected()).To(ContainElements("codes", "codes-dependent", "define", "define-caller"))
		again, err := backend.Apply(ctx, db, retry)
		Expect(err).To(MatchError(ContainSubstring("define fixture")))
		Expect(again.Applied).To(ContainElements("codes", "codes-dependent"))
		Expect(again.Blocked).To(Equal([]string{"untransacted"}))
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

	It("blocks unparsed SQL, missing definitions, unsupported probes and cycles, and only warns about dynamic SQL", func(ctx SpecContext) {
		Expect(execSQL(ctx, db, "CREATE FUNCTION dbo.unstable_context() RETURNS nvarchar(40) AS BEGIN RETURN CONVERT(nvarchar(40),GETDATE()); END")).To(Succeed())
		Expect(execSQL(ctx, db, "CREATE FUNCTION dbo.unsupported_context() RETURNS int AS BEGIN RETURN 1; END")).To(Succeed())
		opts := backend.Options{
			DB: db, Scope: "diagnostics",
			Functions: []backend.Function{{Name: "dbo.unstable_context", Type: "nvarchar"}, {Name: "dbo.unsupported_context", Type: "int"}},
			Scripts: []scripts.Script{
				{ID: "dynamic", Phase: scripts.Post, SQL: "DECLARE @query nvarchar(max) = N'SELECT 1'; EXEC(@query);"},
				{ID: "unparsed", Phase: scripts.Post, SQL: "BULK INSERT dbo.ScriptRuns FROM 'runs.csv';"},
				{ID: "missing", Phase: scripts.Post, SQL: "SELECT dbo.missing_context()"},
				{ID: "unsupported", Phase: scripts.Post, SQL: "SELECT dbo.unsupported_context()"},
				{ID: "unstable", Phase: scripts.Post, SQL: "SELECT dbo.unstable_context()"},
			},
		}
		file := plan(ctx, opts)
		Expect(file.Selected()).To(Equal([]string{"dynamic"}))
		Expect(file.Warnings).To(ContainElement(ContainSubstring("runs dynamic SQL")))
		wanted := map[string]string{"unparsed": "explicit dependencies", "missing": "not found or not visible", "unsupported": "unsupported", "unstable": "unstable builtin"}
		for _, item := range file.Actions {
			if item.Script.ID != "dynamic" {
				Expect(item.Blocked).To(ContainElement(ContainSubstring(wanted[item.Script.ID])))
			}
		}
		opts.Scripts[0].DependsOn = []string{"missing"}
		opts.Scripts[2].DependsOn = []string{"dynamic"}
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
