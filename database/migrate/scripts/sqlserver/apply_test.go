package sqlserver

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// recordedState captures the ledger state a MERGE writes for one script.
type recordedState struct {
	states *[]scripts.State
}

func (r recordedState) Match(v driver.Value) bool {
	text, ok := v.(string)
	if !ok {
		return false
	}
	var state scripts.State
	if err := json.Unmarshal([]byte(text), &state); err != nil {
		return false
	}
	*r.states = append(*r.states, state)
	return true
}

func ledgerRows(states map[string]scripts.State) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"path", "hash", "state"})
	for id, state := range states {
		hash, err := hex.DecodeString(state.Hash)
		Expect(err).NotTo(HaveOccurred())
		encoded, err := json.Marshal(state)
		Expect(err).NotTo(HaveOccurred())
		rows.AddRow(id, hash, string(encoded))
	}
	return rows
}

func expectLedger(mock sqlmock.Sqlmock, scope string, states map[string]scripts.State) {
	mock.ExpectQuery("SELECT CASE").WillReturnRows(sqlmock.NewRows([]string{"exists", "extended"}).AddRow(1, 1))
	mock.ExpectQuery("SELECT path,hash,state").WithArgs(scope).WillReturnRows(ledgerRows(states))
}

// countryModule is dbo.country, which reads dbo.AsEnv per sys.sql_expression_dependencies.
func expectCountryLevel(mock sqlmock.Sqlmock) {
	modules := sqlmock.NewRows([]string{"requested", "schema", "name", "kind", "definition", "ansi", "quoted", "type", "length", "precision", "scale", "parameters"}).
		AddRow("[dbo].[country]", "dbo", "country", "FN", "CREATE FUNCTION dbo.country() RETURNS nvarchar(40) AS BEGIN RETURN (SELECT Country FROM dbo.AsEnv) END", true, true, "nvarchar", 80, 0, 0, 0)
	refs := sqlmock.NewRows([]string{"requested", "server", "db", "schema", "name", "kind", "bound"}).AddRow("[dbo].[country]", "", "", "dbo", "AsEnv", "U", true)
	mock.ExpectQuery(`SELECT v\.name, SCHEMA_NAME`).WithArgs("[dbo].[country]").WillReturnRows(modules)
	mock.ExpectQuery(`SELECT v\.name, COALESCE`).WithArgs("[dbo].[country]").WillReturnRows(refs)
}

func expectProbe(mock sqlmock.Sqlmock, value string) {
	mock.ExpectQuery(`SELECT \[dbo\]\.\[country\]\(\)`).WillReturnRows(sqlmock.NewRows([]string{"country"}).AddRow(value))
}

func expectIdentity(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("SELECT CONVERT").WillReturnRows(sqlmock.NewRows([]string{"server", "database"}).AddRow("example", "disposable"))
}

// expectInspection expects one full observation: identity, ledger, the
// country level and one probe for every script calling it.
func expectInspection(mock sqlmock.Sqlmock, scope string, states map[string]scripts.State, country string) {
	expectIdentity(mock)
	expectLedger(mock, scope, states)
	expectCountryLevel(mock)
	expectProbe(mock, country)
}

// none expects no observation.
func none() {}

// expectRun expects one transactional script up to its after-check: the
// in-progress marker, what the transaction observes before the body, the
// body and what it observes after.
func expectRun(mock sqlmock.Sqlmock, scope, id, body string, before, after func()) {
	expectLedger(mock, scope, nil)
	mock.ExpectExec("MERGE dbo.sqlglot_migrations").WithArgs(scope, id, sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCK_TIMEOUT").WillReturnResult(sqlmock.NewResult(0, 0))
	before()
	mock.ExpectExec(regexp.QuoteMeta(body)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT @@TRANCOUNT`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	after()
}

// expectTransaction expects one transactional script that commits: expectRun,
// then the success record and attempt.
func expectTransaction(mock sqlmock.Sqlmock, scope, id, body string, states *[]scripts.State, before, after func()) {
	expectRun(mock, scope, id, body, before, after)
	mock.ExpectExec("MERGE dbo.sqlglot_migrations").WithArgs(scope, id, sqlmock.AnyArg(), recordedState{states}).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectExec("INSERT INTO dbo.sqlglot_migration_attempts").WithArgs(scope, id, sqlmock.AnyArg(), "applied", "").WillReturnResult(sqlmock.NewResult(0, 1))
}

func expectLock(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("sp_getapplock").WillReturnRows(sqlmock.NewRows([]string{"code"}).AddRow(0))
}

// expectSessionCheck expects the check that the session survived a failure.
func expectSessionCheck(mock sqlmock.Sqlmock) {
	mock.ExpectExec("IF @").WillReturnResult(sqlmock.NewResult(0, 0))
}

func expectRelease(mock sqlmock.Sqlmock) {
	mock.ExpectExec("sp_releaseapplock").WillReturnResult(sqlmock.NewResult(0, 0))
}

// expectStore expects the metadata tables and the plan snapshot an apply
// with selected scripts writes before the first one runs.
func expectStore(mock sqlmock.Sqlmock) {
	mock.ExpectExec("IF OBJECT_ID").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DELETE FROM dbo.sqlglot_observations").WillReturnResult(sqlmock.NewResult(0, 1))
}

// dynamicSQL runs SQL held in a variable, whose effects analysis cannot know.
const dynamicSQL = "DECLARE @sql nvarchar(max) = N'SELECT 1'; EXEC sp_executesql @sql;"

const (
	dynamicUnknownWarning = "script w has unknown effects (line 1: unsupported SQL statement: dynamic SQL); apply re-inspects the plan after it runs"
	ownTransactionWarning = "script PostDeployment_W (w) controls its own transaction inside the migration's: if it ends that transaction, or leaves one open, it is commit-unknown and blocked until reconciled"
)

func reasonsOf(file PlanFile) map[string][]string {
	reasons := map[string][]string{}
	for _, item := range file.Actions {
		reasons[item.Script.ID] = item.Reasons
	}
	return reasons
}

var _ = Describe("Plan file apply", func() {
	asEnv := scripts.NewObjectName("dbo", "AsEnv")
	var opts Options
	var mock sqlmock.Sqlmock
	var db *sql.DB
	// connect replaces the connection, which one Apply uses up.
	connect := func() {
		conn, m, err := sqlmock.New()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(m.ExpectationsWereMet()).To(Succeed()) })
		db, mock, opts.DB = conn, m, conn
	}
	BeforeEach(func() {
		connect()
		// w rewrites dbo.AsEnv; the catalog's dbo.country reads it; s, t and
		// v call dbo.country. Their effects come from analysing their SQL.
		opts = Options{DB: db, Scope: "bundle", Functions: []Function{{Name: "dbo.country", Type: "nvarchar"}}, Scripts: []scripts.Script{
			{ID: "w", Name: "PreDeployment_0_AsEnv", SQL: "UPDATE dbo.AsEnv SET Country=N'MW'", Phase: scripts.Pre},
			{ID: "s", SQL: "SELECT dbo.country() AS s", Phase: scripts.Post},
			{ID: "t", SQL: "SELECT dbo.country() AS t", Phase: scripts.Post},
			{ID: "v", SQL: "SELECT dbo.country() AS v", Phase: scripts.Post},
		}}
	})
	planned := func(ledger map[string]scripts.State, country string) PlanFile {
		expectInspection(mock, opts.Scope, ledger, country)
		file, err := Plan(GinkgoT().Context(), opts)
		Expect(err).NotTo(HaveOccurred())
		return file
	}
	// baseline is the ledger after every script ran against country ZW, with
	// w's source since edited.
	baseline := func() map[string]scripts.State {
		states := map[string]scripts.State{}
		for _, item := range planned(nil, "ZW").Actions {
			states[item.Script.ID] = item.State
		}
		w := states["w"]
		w.SourceHash = scripts.SourceHash("UPDATE dbo.AsEnv SET Country=N'ZW'")
		w.Hash = scripts.StateHash(w)
		states["w"] = w
		return states
	}
	probeCountry := func(value string) func() {
		return func() { expectCountryLevel(mock); expectProbe(mock, value) }
	}

	It("re-probes a function an upstream script writes once after the writer, and never inside its callers' transactions", func(ctx SpecContext) {
		zimbabwe := baseline()["s"].Inputs["result:dbo.country"]
		ledger := map[string]scripts.State{"w": baseline()["w"]}
		file := planned(ledger, "ZW")
		Expect(reasonsOf(file)).To(Equal(map[string][]string{"w": {"source-changed"}, "s": {"new"}, "t": {"new"}, "v": {"new"}}))
		Expect(file.Pretty().String()).To(SatisfyAll(
			ContainSubstring("+ run      s  new"),
			ContainSubstring("~ re-run   w  source-changed"),
			ContainSubstring("Plan: 3 to run, 1 to re-run, 0 blocked, 0 unchanged."),
		))

		var recorded []scripts.State
		expectLock(mock)
		expectInspection(mock, opts.Scope, ledger, "ZW")
		expectStore(mock)
		expectTransaction(mock, opts.Scope, "w", "UPDATE dbo.AsEnv", &recorded, none, none)
		probeCountry("MW")()
		mock.ExpectExec("UPDATE dbo.sqlglot_observations").WithArgs(opts.Scope, file.Hash, "result:dbo.country", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
		for _, id := range []string{"s", "t", "v"} {
			expectTransaction(mock, opts.Scope, id, "SELECT dbo.country() AS "+id, &recorded, none, none)
		}
		expectRelease(mock)

		result, err := Apply(ctx, db, file)

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(Result{Outcome: "applied", Applied: []string{"w", "s", "t", "v"}}))
		malawi := recorded[1].Inputs["result:dbo.country"]
		Expect(malawi).NotTo(Equal(zimbabwe), "s records the country re-probed after w")
		Expect([]string{recorded[2].Inputs["result:dbo.country"], recorded[3].Inputs["result:dbo.country"]}).To(Equal([]string{malawi, malawi}))
		Expect(recorded[1].Hash).To(Equal(scripts.StateHash(recorded[1])))
		Expect(recorded[1].Values).To(HaveKeyWithValue("result:dbo.country", HaveValue(Equal("MW"))), "s records the value it ran with, not the planned one")
	})

	It("leaves to the next plan the callers of a function an upstream script changes", func(ctx SpecContext) {
		ledger := baseline()
		file := planned(ledger, "ZW")
		Expect(file.Selected()).To(Equal([]string{"w"}))
		var recorded []scripts.State
		expectLock(mock)
		expectInspection(mock, opts.Scope, ledger, "ZW")
		expectStore(mock)
		expectTransaction(mock, opts.Scope, "w", "UPDATE dbo.AsEnv", &recorded, none, none)
		expectRelease(mock)

		result, err := Apply(ctx, db, file)

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(Result{Outcome: "applied", Applied: []string{"w"}}))
		ledger["w"] = recorded[0]
		connect()
		next := planned(ledger, "MW")
		Expect(reasonsOf(next)).To(Equal(map[string][]string{"w": nil, "s": {"function-result-changed"}, "t": {"function-result-changed"}, "v": {"function-result-changed"}}))
	})

	Describe("the reads of a script", func() {
		const condition = "SELECT CASE WHEN NOT EXISTS (SELECT 1 FROM dbo.AsCode WHERE CodeName='A') THEN 1 ELSE 0 END"
		batch := regexp.QuoteMeta("SET NOCOUNT ON;\nSELECT 0 AS sqlglot_read;\nBEGIN TRY EXEC sys.sp_executesql N'" +
			"SELECT CASE WHEN NOT EXISTS (SELECT 1 FROM dbo.AsCode WHERE CodeName=''A'') THEN 1 ELSE 0 END' END TRY BEGIN CATCH SELECT ERROR_MESSAGE() AS sqlglot_error END CATCH;")
		answered := func(column string, value any) {
			mock.ExpectQuery(batch).WillReturnRows(sqlmock.NewRows([]string{"sqlglot_read"}).AddRow(0), sqlmock.NewRows([]string{column}).AddRow(value))
		}
		plan := func(states map[string]scripts.State, answer func()) PlanFile {
			expectIdentity(mock)
			expectLedger(mock, opts.Scope, states)
			answer()
			file, err := Plan(GinkgoT().Context(), opts)
			Expect(err).NotTo(HaveOccurred())
			return file
		}
		// ran is the state r records once it ran and its read answered.
		ran := func(answer func()) scripts.State {
			opts.Functions = nil
			opts.Scripts = []scripts.Script{{ID: "r", SQL: "IF NOT EXISTS (SELECT 1 FROM dbo.AsCode WHERE CodeName='A')\nINSERT dbo.AsCode VALUES ('A')", Phase: scripts.Post}}
			file := plan(nil, none)
			Expect(file.Actions[0].Reads).To(Equal([]scripts.PlannedRead{{Read: scripts.Read{SQL: condition, Objects: []scripts.ObjectName{scripts.NewObjectName("dbo", "AsCode")}}}}))
			var recorded []scripts.State
			expectLock(mock)
			expectIdentity(mock)
			expectLedger(mock, opts.Scope, nil)
			expectStore(mock)
			expectRun(mock, opts.Scope, "r", "IF NOT EXISTS", none, none)
			mock.ExpectExec("MERGE dbo.sqlglot_migrations").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			answer()
			mock.ExpectExec("MERGE dbo.sqlglot_migrations").WithArgs(opts.Scope, "r", sqlmock.AnyArg(), recordedState{&recorded}).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("INSERT INTO dbo.sqlglot_migration_attempts").WillReturnResult(sqlmock.NewResult(0, 1))
			expectRelease(mock)
			result, err := Apply(GinkgoT().Context(), db, file)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Outcome).To(Equal("applied"))
			connect()
			return recorded[0]
		}

		DescribeTable("select it when they answer differently than after it ran, showing both answers in the plan",
			func(column string, value any, reasons []string, want func() *scripts.ReadAnswer, shown string) {
				ledger := map[string]scripts.State{"r": ran(func() { answered("", int64(0)) })}
				file := plan(ledger, func() { answered(column, value) })

				Expect(file.Actions[0].Reasons).To(Equal(reasons))
				Expect(file.Actions[0].Reads[0].Recorded).To(HaveField("Value", HaveValue(Equal("0"))))
				Expect(file.Actions[0].Reads[0].Answer).To(HaveField("Value", want().Value))
				Expect(file.Actions[0].Reads[0].Answer.Error).To(Equal(want().Error))
				Expect(file.Pretty().String()).To(ContainSubstring(shown))
				Expect(file.Verify()).To(Succeed())
			},
			Entry("the same answer", "", int64(0), nil, func() *scripts.ReadAnswer { return &scripts.ReadAnswer{Value: new("0")} }, "0 to re-run, 0 blocked, 1 unchanged"),
			Entry("another answer", "", int64(1), []string{"read-changed"}, func() *scripts.ReadAnswer { return &scripts.ReadAnswer{Value: new("1")} }, `~ reads `+condition+`  "0" → "1"`),
			Entry("an error", "sqlglot_error", "Invalid object name 'dbo.AsCode'.", nil, func() *scripts.ReadAnswer { return &scripts.ReadAnswer{Error: "Invalid object name 'dbo.AsCode'."} }, "1 unchanged"),
		)

		It("fail the plan when the batch answering them stops early", func(ctx SpecContext) {
			ledger := map[string]scripts.State{"r": ran(func() { answered("", int64(0)) })}
			expectIdentity(mock)
			expectLedger(mock, opts.Scope, ledger)
			mock.ExpectQuery(batch).WillReturnRows(sqlmock.NewRows([]string{"sqlglot_read"}).AddRow(0))
			_, err := Plan(ctx, opts)
			Expect(err).To(MatchError("answer what the scripts read: read 0 answered no result set"))
		})

		It("are not compared again once one could not be answered after its run", func() {
			ledger := map[string]scripts.State{"r": ran(func() { answered("sqlglot_error", "Permission denied") })}
			Expect(plan(ledger, none).Selected()).To(BeEmpty())
		})
	})

	DescribeTable("re-runs a script reading the environment only when the call's value changes",
		func(ctx SpecContext, database, country string, reasons []string, shown string) {
			opts.Functions = nil
			opts.Scripts = []scripts.Script{
				{ID: "country", SQL: "IF (SELECT SUBSTRING((SELECT DB_NAME()),5,3)) = N'ZIM' UPDATE dbo.AsEnv SET Country=N'ZW'", Phase: scripts.Post},
				{ID: "other", SQL: "UPDATE dbo.AsCode SET CodeValue=N'1'", Phase: scripts.Post},
			}
			plan := func(database, country string, states map[string]scripts.State) PlanFile {
				mock.ExpectQuery("SELECT CONVERT").WillReturnRows(sqlmock.NewRows([]string{"server", "database"}).AddRow("example", database))
				expectLedger(mock, opts.Scope, states)
				mock.ExpectQuery(regexp.QuoteMeta("SELECT CONVERT(nvarchar(4000), SUBSTRING(DB_NAME(), 5, 3))")).
					WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(country))
				file, err := Plan(ctx, opts)
				Expect(err).NotTo(HaveOccurred())
				return file
			}
			states := map[string]scripts.State{}
			for _, item := range plan("LAB_ZIM_OIPA_QA", "ZIM", nil).Actions {
				states[item.Script.ID] = item.State
			}

			file := plan(database, country, states)

			Expect(reasonsOf(file)).To(Equal(map[string][]string{"country": reasons, "other": nil}))
			Expect(file.Pretty().String()).To(ContainSubstring(shown))
		},
		Entry("another database, the same value", "LAB_ZIM_OIPA_UAT", "ZIM", nil, `= SUBSTRING(DB_NAME(), 5, 3) = "ZIM"  used by 1`),
		Entry("another value", "LAB_MWI_OIPA_QA", "MWI", []string{"builtin-changed"}, `~ SUBSTRING(DB_NAME(), 5, 3) = "MWI" (was "ZIM")  used by 1, changed for 1`),
	)

	DescribeTable("refuses a plan the target moved away from before anything runs",
		func(ctx SpecContext, move func(ledger map[string]scripts.State) (map[string]scripts.State, string), moved string) {
			opts.Scripts = opts.Scripts[1:2]
			file := planned(nil, "ZW")
			ledger, country := move(map[string]scripts.State{})
			expectLock(mock)
			expectInspection(mock, opts.Scope, ledger, country)
			expectRelease(mock)

			result, err := Apply(ctx, db, file)

			var stale *StalePlanError
			Expect(errors.As(err, &stale)).To(BeTrue(), "a stale plan answers a StalePlanError")
			Expect(stale.Moved).To(ContainElement(moved))
			Expect(err).To(MatchError(ContainSubstring("run sqlglot migrate plan again")))
			Expect(result).To(Equal(Result{Outcome: "stale", Applied: []string{}}))
		},
		Entry("a function result changed", func(ledger map[string]scripts.State) (map[string]scripts.State, string) {
			return ledger, "MW"
		}, "observed input result:dbo.country changed"),
		Entry("a ledger row moved", func(ledger map[string]scripts.State) (map[string]scripts.State, string) {
			ledger["s"] = scripts.State{Version: scripts.Version, SourceHash: scripts.SourceHash("SELECT dbo.country() AS s"), Inputs: map[string]string{}, Outcome: "applied"}
			s := ledger["s"]
			s.Hash = scripts.StateHash(s)
			ledger["s"] = s
			return ledger, "ZW"
		}, "ledger row s changed"),
	)

	It("refuses a re-sealed plan whose script was edited after planning", func(ctx SpecContext) {
		opts.Scripts = opts.Scripts[1:2]
		file := planned(nil, "ZW")
		file.Bundle.Scripts[0].SQL = "SELECT dbo.country() AS edited"
		file, err := file.seal()
		Expect(err).NotTo(HaveOccurred())
		expectLock(mock)
		expectInspection(mock, opts.Scope, nil, "ZW")
		expectRelease(mock)

		_, err = Apply(ctx, db, file)

		Expect(err).To(MatchError(ContainSubstring("plan is stale: action s source, policy or inputs changed")))
	})

	It("refuses a tampered plan file without connecting", func(ctx SpecContext) {
		file := planned(nil, "ZW")
		file.Actions[0].Selected = false

		_, err := Apply(ctx, db, file)

		Expect(err).To(MatchError(ContainSubstring("plan file integrity check failed")))
	})

	It("observes after its body the function result a script's own writes reach, failing when the script changed it", func(ctx SpecContext) {
		opts.Scripts = []scripts.Script{{ID: "self", SQL: "UPDATE dbo.AsEnv SET Country=dbo.country()", Phase: scripts.Post}}
		file := planned(nil, "ZW")
		Expect(file.Bundle.Scripts[0].Effects.Writes).To(ContainElement(asEnv), "the analysis finds the UPDATE target")
		Expect(file.Actions[0].SelfWritten).To(Equal([]string{"result:dbo.country"}))

		expectLock(mock)
		expectInspection(mock, opts.Scope, nil, "ZW")
		expectStore(mock)
		expectRun(mock, opts.Scope, "self", "UPDATE dbo.AsEnv", none, probeCountry("MW"))
		mock.ExpectRollback()
		mock.ExpectExec("MERGE dbo.sqlglot_migrations").WithArgs(opts.Scope, "self", sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("INSERT INTO dbo.sqlglot_migration_attempts").WithArgs(opts.Scope, "self", sqlmock.AnyArg(), "failed", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
		expectSessionCheck(mock)
		expectRelease(mock)

		result, err := Apply(ctx, db, file)

		const drift = "script self dependency drift: observed inputs changed (result:dbo.country)"
		Expect(err).To(MatchError(drift))
		Expect(result).To(Equal(Result{Outcome: "failed", Applied: []string{}, Failed: []ScriptFailure{{Script: "self", Outcome: "failed", Error: drift}}}))
	})

	Describe("a script that fails", func() {
		const violation = "Violation of PRIMARY KEY constraint 'PK_ASCODE'"
		// expectFailure expects id's body to fail and its failure to be recorded.
		expectFailure := func(id, body string) {
			expectLedger(mock, opts.Scope, nil)
			mock.ExpectExec("MERGE dbo.sqlglot_migrations").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectBegin()
			mock.ExpectExec("SET LOCK_TIMEOUT").WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectExec(regexp.QuoteMeta(body)).WillReturnError(errors.New(violation))
			mock.ExpectRollback()
			mock.ExpectExec("MERGE dbo.sqlglot_migrations").WithArgs(opts.Scope, id, sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("INSERT INTO dbo.sqlglot_migration_attempts").WithArgs(opts.Scope, id, sqlmock.AnyArg(), "failed", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
		}
		insert := func(id string) string { return "INSERT dbo.AsCode VALUES (N'" + id + "')" }
		failedWith := func(id string) ScriptFailure {
			return ScriptFailure{Script: id, Outcome: "failed", Error: "script " + id + " batch 1: " + violation}
		}
		start := func(bundle ...scripts.Script) PlanFile {
			opts.Functions, opts.Scripts = nil, bundle
			expectIdentity(mock)
			expectLedger(mock, opts.Scope, nil)
			file, err := Plan(GinkgoT().Context(), opts)
			Expect(err).NotTo(HaveOccurred())
			expectLock(mock)
			expectIdentity(mock)
			expectLedger(mock, opts.Scope, nil)
			expectStore(mock)
			return file
		}

		It("skips only the scripts that depend on it, reporting each, and runs the rest", func(ctx SpecContext) {
			file := start(
				scripts.Script{ID: "a", Phase: scripts.Post, SQL: insert("a")},
				scripts.Script{ID: "b", Phase: scripts.Post, SQL: insert("b"), DependsOn: []string{"a"}},
				scripts.Script{ID: "c", Phase: scripts.Post, SQL: insert("c"), DependsOn: []string{"b"}},
				scripts.Script{ID: "d", Phase: scripts.Post, SQL: "UPDATE dbo.AsCode SET Seen=(SELECT COUNT(*) FROM dbo.AsCode)"},
			)
			d := file.action("d")
			var recorded []scripts.State
			expectFailure("a", insert("a"))
			expectSessionCheck(mock)
			expectTransaction(mock, opts.Scope, "d", "UPDATE dbo.AsCode", &recorded, none, none)
			expectRelease(mock)

			var skipped []error
			result, err := Apply(ctx, db, file, WithProgress(func(p Progress) {
				var dependency *DependencyError
				if errors.As(p.Err, &dependency) {
					skipped = append(skipped, dependency)
				}
			}))

			Expect(d.UntrackedReads).To(Equal([]scripts.ObjectName{scripts.NewObjectName("dbo", "AsCode")}), "reading a table a failed script writes is no dependency")
			Expect(err).To(MatchError(failedWith("a").Error))
			Expect(result).To(Equal(Result{Outcome: "failed", Applied: []string{"d"}, Failed: []ScriptFailure{failedWith("a")},
				Skipped: []SkippedScript{{Script: "b", Dependency: "a"}, {Script: "c", Dependency: "b"}}}))
			Expect(skipped).To(HaveExactElements(MatchError("script b was not run: it depends on a, which failed or was skipped"), HaveOccurred()))
		})

		It("returns every failure", func(ctx SpecContext) {
			file := start(scripts.Script{ID: "a", Phase: scripts.Post, SQL: insert("a")}, scripts.Script{ID: "b", Phase: scripts.Post, SQL: insert("b")})
			for _, id := range []string{"a", "b"} {
				expectFailure(id, insert(id))
				expectSessionCheck(mock)
			}
			expectRelease(mock)

			result, err := Apply(ctx, db, file)

			Expect(err).To(MatchError(SatisfyAll(ContainSubstring(failedWith("a").Error), ContainSubstring(failedWith("b").Error))))
			Expect(result.Failed).To(Equal([]ScriptFailure{failedWith("a"), failedWith("b")}))
		})

		It("stops the apply when the session did not survive it", func(ctx SpecContext) {
			file := start(scripts.Script{ID: "a", Phase: scripts.Post, SQL: insert("a")}, scripts.Script{ID: "b", Phase: scripts.Post, SQL: insert("b")})
			expectFailure("a", insert("a"))
			mock.ExpectExec("IF @@TRANCOUNT").WillReturnError(errors.New("connection reset"))
			expectRelease(mock)

			result, err := Apply(ctx, db, file)

			Expect(err).To(MatchError(ContainSubstring("the session did not survive script a: connection reset")))
			Expect(result.Failed).To(Equal([]ScriptFailure{failedWith("a")}))
			Expect(result.Applied).To(BeEmpty())
		})

		It("runs the other pre scripts but never the barrier after a pre script failed", func(ctx SpecContext) {
			file := start(scripts.Script{ID: "a", Phase: scripts.Pre, SQL: insert("a")}, scripts.Script{ID: "b", Phase: scripts.Pre, SQL: insert("b")},
				scripts.Script{ID: "c", Phase: scripts.Post, SQL: insert("c")})
			var recorded []scripts.State
			expectFailure("a", insert("a"))
			expectSessionCheck(mock)
			expectTransaction(mock, opts.Scope, "b", insert("b"), &recorded, none, none)
			expectRelease(mock)

			result, err := Apply(ctx, db, file, WithBarrier(func(context.Context, Connection) error {
				Fail("the barrier ran after a pre script failed")
				return nil
			}))

			Expect(err).To(MatchError(failedWith("a").Error))
			Expect(result.Applied).To(Equal([]string{"b"}))
		})
	})

	It("re-plans after a script with unknown effects and observes every input of the scripts after it", func(ctx SpecContext) {
		opts.Scripts = []scripts.Script{
			{ID: "dynamic", SQL: dynamicSQL + " SELECT dbo.country() AS dynamic", Phase: scripts.Pre, ExplicitDependencies: true},
			opts.Scripts[1],
		}
		file := planned(nil, "ZW")
		Expect(file.Actions[0].EffectsUnknown).NotTo(BeEmpty())
		Expect(file.Pretty().String()).To(ContainSubstring("? effects unknown "))

		var recorded []scripts.State
		expectLock(mock)
		expectInspection(mock, opts.Scope, nil, "ZW")
		expectStore(mock)
		expectTransaction(mock, opts.Scope, "dynamic", "SELECT dbo.country() AS dynamic", &recorded, probeCountry("ZW"), probeCountry("ZW"))
		expectInspection(mock, opts.Scope, nil, "ZW")
		expectTransaction(mock, opts.Scope, "s", "SELECT dbo.country() AS s", &recorded, probeCountry("ZW"), probeCountry("ZW"))
		expectRelease(mock)

		var seen []Progress
		result, err := Apply(ctx, db, file, WithProgress(func(p Progress) { seen = append(seen, p) }))

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(Result{Outcome: "applied", Applied: []string{"dynamic", "s"}}))
		Expect(seen).To(Equal([]Progress{
			{Step: ProgressCheck}, {Step: ProgressCheck, Done: true},
			{Step: ProgressScript, Script: "dynamic"}, {Step: ProgressScript, Script: "dynamic", Done: true},
			{Step: ProgressCheck, Script: "dynamic"}, {Step: ProgressCheck, Script: "dynamic", Done: true},
			{Step: ProgressScript, Script: "s"}, {Step: ProgressScript, Script: "s", Done: true},
		}))
	})

	It("stops as stale after a script with unknown effects whose re-plan blocks a script the file did not", func(ctx SpecContext) {
		opts.Functions = nil
		opts.Scripts = []scripts.Script{
			{ID: "dynamic", SQL: dynamicSQL, Phase: scripts.Pre},
			{ID: "known", SQL: "SELECT 1 AS known", Phase: scripts.Post},
			{ID: "late", SQL: "SELECT 2 AS late", Phase: scripts.Post},
		}
		inspect := func(ledger map[string]scripts.State) {
			expectIdentity(mock)
			expectLedger(mock, opts.Scope, ledger)
		}
		inspect(nil)
		first, err := Plan(ctx, opts)
		Expect(err).NotTo(HaveOccurred())
		late := first.Actions[2].State
		late.Outcome = "partial"
		inspect(nil)
		file, err := Plan(ctx, opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(file.Warnings).To(HaveLen(1))

		var recorded []scripts.State
		expectLock(mock)
		inspect(nil)
		expectStore(mock)
		expectTransaction(mock, opts.Scope, "dynamic", "EXEC sp_executesql", &recorded, none, none)
		inspect(map[string]scripts.State{"late": late})
		expectRelease(mock)

		result, err := Apply(ctx, db, file)

		Expect(err).To(MatchError("plan is stale: script dynamic with unknown effects changed the plan; late is now blocked by reconciliation required: partial; run sqlglot migrate plan again"))
		Expect(result).To(Equal(Result{Outcome: "stale", Applied: []string{"dynamic"}}))
	})

	It("refuses as stale a script with unknown effects whose input drifted in its transaction, restoring its ledger record", func(ctx SpecContext) {
		opts.Scripts = []scripts.Script{{ID: "s", SQL: dynamicSQL + " SELECT dbo.country() AS s", Phase: scripts.Post, ExplicitDependencies: true}}
		file := planned(nil, "ZW")

		expectLock(mock)
		expectInspection(mock, opts.Scope, nil, "ZW")
		expectStore(mock)
		expectLedger(mock, opts.Scope, nil)
		mock.ExpectExec("MERGE dbo.sqlglot_migrations").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectBegin()
		mock.ExpectExec("SET LOCK_TIMEOUT").WillReturnResult(sqlmock.NewResult(0, 0))
		probeCountry("MW")()
		mock.ExpectRollback()
		mock.ExpectExec("DELETE FROM dbo.sqlglot_migrations").WithArgs(opts.Scope, "s").WillReturnResult(sqlmock.NewResult(0, 1))
		expectRelease(mock)

		result, err := Apply(ctx, db, file)

		Expect(err).To(MatchError(ContainSubstring("plan is stale: script s dependency drift")))
		Expect(result).To(Equal(Result{Outcome: "stale", Applied: []string{}}))
	})

	It("runs a nontransactional caller of a function without observing it again, warning in the plan", func(ctx SpecContext) {
		opts.Scripts = opts.Scripts[1:2]
		opts.Scripts[0].NonTransactional = true
		const bundleWarning = "script t is left out of the bundle"
		opts.Warnings = []string{bundleWarning}
		file := planned(nil, "ZW")
		Expect(file.Warnings).To(Equal([]string{bundleWarning, "script s runs outside a transaction: a failure leaves it partial, blocked until reconciled"}))
		Expect(file.Verify()).To(Succeed())

		var recorded []scripts.State
		expectLock(mock)
		expectInspection(mock, opts.Scope, nil, "ZW")
		expectStore(mock)
		expectLedger(mock, opts.Scope, nil)
		mock.ExpectExec("MERGE dbo.sqlglot_migrations").WithArgs(opts.Scope, "s", sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("SELECT dbo.country() AS s")).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("MERGE dbo.sqlglot_migrations").WithArgs(opts.Scope, "s", sqlmock.AnyArg(), recordedState{&recorded}).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("INSERT INTO dbo.sqlglot_migration_attempts").WithArgs(opts.Scope, "s", sqlmock.AnyArg(), "applied", "").WillReturnResult(sqlmock.NewResult(0, 1))
		expectRelease(mock)

		result, err := Apply(ctx, db, file)

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(Result{Outcome: "applied", Applied: []string{"s"}}))
		Expect(recorded[0].Inputs).To(Equal(file.Actions[0].State.Inputs))
	})

	DescribeTable("selects a script it can only warn about, naming it",
		func(script scripts.Script, warnings ...string) {
			script.ID, script.Name, script.Phase = "w", "PostDeployment_W", scripts.Post
			opts.Scripts = []scripts.Script{script}
			expectIdentity(mock)
			expectLedger(mock, opts.Scope, nil)
			expectCountryLevel(mock)

			file, err := Plan(GinkgoT().Context(), opts)

			Expect(err).NotTo(HaveOccurred())
			Expect([]any{file.Selected(), file.Actions[0].Blocked}).To(Equal([]any{[]string{"w"}, []string(nil)}))
			Expect(file.Warnings).To(Equal(warnings))
		},
		Entry("it controls its own transaction", scripts.Script{SQL: "BEGIN TRANSACTION\nUPDATE dbo.AsFund SET Code = 'x'\nCOMMIT"}, ownTransactionWarning),
		Entry("it is not transactional", scripts.Script{SQL: "BEGIN TRANSACTION\nUPDATE dbo.AsFund SET Code = 'x'\nCOMMIT", NonTransactional: true},
			"script w runs outside a transaction: a failure leaves it partial, blocked until reconciled"),
		Entry("it runs dynamic SQL", scripts.Script{SQL: dynamicSQL}, dynamicUnknownWarning,
			"script PostDeployment_W (w) runs dynamic SQL: what it reads is not tracked, so a change to it does not run the script again"),
		Entry("it runs dynamic SQL whose dependencies are declared", scripts.Script{SQL: dynamicSQL, ExplicitDependencies: true}, dynamicUnknownWarning),
	)

	It("leaves a blocked script out of the warnings", func(ctx SpecContext) {
		opts.Scripts = []scripts.Script{{ID: "w", Phase: scripts.Post, SQL: "BEGIN TRANSACTION\nSELECT dbo.retired()\nCOMMIT"}}
		expectIdentity(mock)
		expectLedger(mock, opts.Scope, nil)
		expectLevel(mock, []string{"dbo.country", "dbo.retired"}, function("dbo.country", "N'ZW'"))

		file, err := Plan(ctx, opts)

		Expect(err).NotTo(HaveOccurred())
		Expect(file.Actions[0].Blocked).NotTo(BeEmpty())
		Expect(file.Warnings).To(BeEmpty())
	})

	DescribeTable("runs a script whose own transaction spans batches",
		func(ctx SpecContext, committed bool, counts ...int) {
			opts.Functions = nil
			opts.Scripts = []scripts.Script{{ID: "w", Phase: scripts.Post, SQL: "BEGIN TRANSACTION\nGO\nCOMMIT"}}
			expectIdentity(mock)
			expectLedger(mock, opts.Scope, nil)
			file, err := Plan(ctx, opts)
			Expect(err).NotTo(HaveOccurred())

			expectLock(mock)
			expectIdentity(mock)
			expectLedger(mock, opts.Scope, nil)
			expectStore(mock)
			expectLedger(mock, opts.Scope, nil)
			mock.ExpectExec("MERGE dbo.sqlglot_migrations").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectBegin()
			mock.ExpectExec("SET LOCK_TIMEOUT").WillReturnResult(sqlmock.NewResult(0, 0))
			for i, batch := range []string{"BEGIN TRANSACTION", "COMMIT"} {
				mock.ExpectExec(batch).WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectQuery(`SELECT @@TRANCOUNT`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(counts[i]))
			}
			if committed {
				mock.ExpectExec("MERGE dbo.sqlglot_migrations").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
				mock.ExpectExec("INSERT INTO dbo.sqlglot_migration_attempts").WithArgs(opts.Scope, "w", sqlmock.AnyArg(), "applied", "").WillReturnResult(sqlmock.NewResult(0, 1))
			} else {
				mock.ExpectRollback()
				mock.ExpectExec("INSERT INTO dbo.sqlglot_migration_attempts").WithArgs(opts.Scope, "w", sqlmock.AnyArg(), "commit-unknown", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
				expectSessionCheck(mock)
			}
			expectRelease(mock)

			var last Progress
			result, err := Apply(ctx, db, file, WithProgress(func(p Progress) { last = p }))

			Expect([]any{last.Step, last.Script, last.Done, last.Err == nil}).To(Equal([]any{ProgressScript, "w", true, committed}))
			if committed {
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Applied).To(Equal([]string{"w"}))
			} else {
				Expect(err).To(MatchError(ContainSubstring("script changed the migration transaction count to 2")))
				Expect(result.Applied).To(BeEmpty())
			}
		},
		Entry("committing what it began", true, 2, 1),
		Entry("refusing one it leaves open", false, 2, 2),
	)

	It("selects a legacy content-only ledger row whose script now observes an input", func(ctx SpecContext) {
		opts.Scripts = opts.Scripts[1:2]
		sum := sha256.Sum256([]byte(opts.Scripts[0].SQL))
		expectIdentity(mock)
		mock.ExpectQuery("SELECT CASE").WillReturnRows(sqlmock.NewRows([]string{"exists", "extended"}).AddRow(1, 0))
		mock.ExpectQuery("SELECT path,hash,CAST").WithArgs(opts.Scope).WillReturnRows(sqlmock.NewRows([]string{"path", "hash", "state"}).AddRow("s", sum[:], nil))
		probeCountry("ZW")()

		file, err := Plan(ctx, opts)

		Expect(err).NotTo(HaveOccurred())
		action := file.Actions[0]
		Expect([]any{action.Selected, action.Reasons, action.Blocked}).To(Equal([]any{true, []string{"inputs-unrecorded"}, []string(nil)}))
	})

	It("reads a version 2 ledger row as current", func(ctx SpecContext) {
		state := scripts.State{Version: 2, SourceHash: "source", Inputs: map[string]string{}}
		state.Hash = scripts.StateHash(state)
		expectLedger(mock, "legacy", map[string]scripts.State{"seed": state})

		states, err := readStates(ctx, db, ledgerScope{Scope: "legacy"})

		Expect(err).NotTo(HaveOccurred())
		Expect(states).To(Equal(map[string]scripts.State{"seed": state}))
	})
})
