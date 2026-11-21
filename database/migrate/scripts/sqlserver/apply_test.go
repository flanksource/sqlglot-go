package sqlserver

import (
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
	BeforeEach(func() {
		conn, m, err := sqlmock.New()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(m.ExpectationsWereMet()).To(Succeed()) })
		db, mock = conn, m
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

	It("re-runs every caller of a function an upstream script writes, re-probing it once after the writer and never inside the callers' transactions", func(ctx SpecContext) {
		ledger := baseline()
		file := planned(ledger, "ZW")
		upstream := []string{"upstream-write:dbo.asenv"}
		Expect(reasonsOf(file)).To(Equal(map[string][]string{"w": {"source-changed"}, "s": upstream, "t": upstream, "v": upstream}))
		Expect(file.Pretty().String()).To(SatisfyAll(
			ContainSubstring("~ re-run   s  upstream-write: dbo.asenv ← PreDeployment_0_AsEnv"),
			ContainSubstring("~ re-run   w  source-changed"),
			ContainSubstring("Plan: 0 to run, 4 to re-run, 0 blocked, 0 unchanged."),
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
		Expect(malawi).NotTo(Equal(ledger["s"].Inputs["result:dbo.country"]), "s records the country re-probed after w")
		Expect([]string{recorded[2].Inputs["result:dbo.country"], recorded[3].Inputs["result:dbo.country"]}).To(Equal([]string{malawi, malawi}))
		Expect(recorded[1].Hash).To(Equal(scripts.StateHash(recorded[1])))
	})

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
		expectRelease(mock)

		result, err := Apply(ctx, db, file)

		Expect(err).To(MatchError("script self dependency drift: observed inputs changed (result:dbo.country)"))
		Expect(result).To(Equal(Result{Outcome: "failed", Applied: []string{}}))
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

		result, err := Apply(ctx, db, file)

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(Result{Outcome: "applied", Applied: []string{"dynamic", "s"}}))
	})

	It("stops as stale after a script with unknown effects whose re-plan selects work the file did not", func(ctx SpecContext) {
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
		late := map[string]scripts.State{"late": first.Actions[2].State}
		inspect(late)
		file, err := Plan(ctx, opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(file.Warnings).To(HaveLen(1))

		var recorded []scripts.State
		expectLock(mock)
		inspect(late)
		expectStore(mock)
		expectTransaction(mock, opts.Scope, "dynamic", "EXEC sp_executesql", &recorded, none, none)
		// The re-plan finds late's record gone, so late is new work the file
		// never selected.
		inspect(nil)
		expectRelease(mock)

		result, err := Apply(ctx, db, file)

		Expect(err).To(MatchError("plan is stale: script dynamic with unknown effects changed the plan; late now runs for new; run sqlglot migrate plan again"))
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
		file := planned(nil, "ZW")
		Expect(file.Warnings).To(Equal([]string{"script s runs outside a transaction: a failure leaves it partial, blocked until reconciled"}))

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

	It("adopts a legacy ledger row from a plan file without running its body", func(ctx SpecContext) {
		opts.Scripts = opts.Scripts[1:2]
		legacy := func() {
			sum := sha256.Sum256([]byte(opts.Scripts[0].SQL))
			expectIdentity(mock)
			mock.ExpectQuery("SELECT CASE").WillReturnRows(sqlmock.NewRows([]string{"exists", "extended"}).AddRow(1, 0))
			mock.ExpectQuery("SELECT path,hash,CAST").WithArgs(opts.Scope).WillReturnRows(sqlmock.NewRows([]string{"path", "hash", "state"}).AddRow("s", sum[:], nil))
			probeCountry("ZW")()
		}
		legacy()
		file, err := Plan(ctx, opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(file.Actions[0].Blocked).To(Equal([]string{"adoption-required"}))
		adoption, err := AdoptionPlan(file, []AdoptionEvidence{{ID: "s", SourceHash: file.Actions[0].State.SourceHash, Provenance: "seeded by hand", Query: "SELECT N'ZW'", Expected: "ZW"}})
		Expect(err).NotTo(HaveOccurred())

		var recorded []scripts.State
		expectLock(mock)
		legacy()
		mock.ExpectExec("IF OBJECT_ID").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT N'ZW'")).WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("ZW"))
		mock.ExpectExec("MERGE dbo.sqlglot_migrations").WithArgs(opts.Scope, "s", sqlmock.AnyArg(), recordedState{&recorded}).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		expectRelease(mock)

		Expect(Adopt(ctx, db, AdoptionOptions{Adoption: adoption})).To(Succeed())
		Expect(recorded).To(HaveLen(1))
		Expect([]string{recorded[0].Outcome, recorded[0].Provenance}).To(Equal([]string{"adopted", "seeded by hand"}))
		Expect(recorded[0].Inputs).To(Equal(file.Actions[0].State.Inputs))
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
