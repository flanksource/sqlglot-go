package sqlserver

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"iter"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/flanksource/clicky/cache"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// fakeModule is one routine the mocked catalog holds; refs are the
// routines sys.sql_expression_dependencies lists for it.
type fakeModule struct {
	name, definition string
	kind             string
	refs             []string
}

func function(name string, body string, refs ...string) fakeModule {
	return fakeModule{name: name, kind: "FN", refs: refs, definition: "CREATE FUNCTION " + name + "() RETURNS nvarchar(40) AS BEGIN RETURN " + body + " END"}
}

func quote(name string) string {
	q, err := scripts.QuoteName(name)
	Expect(err).NotTo(HaveOccurred())
	return q
}

func bound(names []string) []driver.Value {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = quote(name)
	}
	slices.Sort(quoted)
	out := make([]driver.Value, len(quoted))
	for i, q := range quoted {
		out[i] = q
	}
	return out
}

func expectTarget(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("SELECT CONVERT").WillReturnRows(sqlmock.NewRows([]string{"server", "database"}).AddRow("example", "disposable"))
	mock.ExpectQuery("SELECT CASE").WillReturnRows(sqlmock.NewRows([]string{"exists", "extended"}).AddRow(0, 0))
}

// expectLevel expects one graph level: one ReadModules over requested and one
// ReadModuleReferences over the modules found.
func expectLevel(mock sqlmock.Sqlmock, requested []string, found ...fakeModule) {
	modules := sqlmock.NewRows([]string{"requested", "schema", "name", "kind", "definition", "ansi", "quoted", "type", "length", "precision", "scale", "parameters"})
	refs := sqlmock.NewRows([]string{"requested", "server", "db", "schema", "name", "kind", "bound"})
	var names []string
	for _, m := range found {
		schema, name, _ := strings.Cut(m.name, ".")
		modules.AddRow(quote(m.name), schema, name, m.kind, m.definition, true, true, "nvarchar", 80, 0, 0, 0)
		for _, ref := range m.refs {
			refSchema, refName, _ := strings.Cut(ref, ".")
			refs.AddRow(quote(m.name), "", "", refSchema, refName, "FN", true)
		}
		names = append(names, m.name)
	}
	mock.ExpectQuery(`SELECT v\.name, SCHEMA_NAME`).WithArgs(bound(requested)...).WillReturnRows(modules)
	if len(found) > 0 {
		mock.ExpectQuery(`SELECT v\.name, COALESCE`).WithArgs(bound(names)...).WillReturnRows(refs)
	}
}

// countingStore counts the reads and writes an analysis cache serves.
type countingStore struct {
	cache.Store
	mgets, sets int
	mgetErr     error
}

func (c *countingStore) MGet(ctx context.Context, keys iter.Seq[string]) iter.Seq2[cache.Entry, error] {
	c.mgets++
	if c.mgetErr != nil {
		return func(yield func(cache.Entry, error) bool) { yield(cache.Entry{}, c.mgetErr) }
	}
	return c.Store.MGet(ctx, keys)
}

func (c *countingStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	c.sets++
	return c.Store.Set(ctx, key, value, ttl)
}

func definitionDependencies(names ...string) []scripts.Dependency {
	out := make([]scripts.Dependency, len(names))
	for i, name := range names {
		out[i] = scripts.Dependency{Kind: "function-definition", Name: name}
	}
	return out
}

var _ = Describe("Script analysis cache", func() {
	var opts Options
	var mock sqlmock.Sqlmock
	country := function("dbo.country", "N'ZW'")
	BeforeEach(func() {
		db, m, err := sqlmock.New()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			Expect(m.ExpectationsWereMet()).To(Succeed())
			m.ExpectClose()
			Expect(db.Close()).To(Succeed())
		})
		mock = m
		opts = Options{DB: db, Scope: "cache", Scripts: []scripts.Script{
			{ID: "seed", SQL: "SELECT dbo.country()", Phase: scripts.Pre, Dependencies: definitionDependencies("dbo.country")},
			{ID: "report", SQL: "SELECT dbo.country()\nGO\nSELECT 1", Phase: scripts.Post, Dependencies: definitionDependencies("dbo.country")},
		}}
	})
	inspect := func(ctx context.Context, opts Options) PlanFile {
		expectTarget(mock)
		expectLevel(mock, []string{"dbo.country"}, country)
		file, err := Plan(ctx, opts)
		Expect(err).NotTo(HaveOccurred())
		return file
	}

	It("answers a second inspection from one read per level with no analysis and the same plan", func(ctx SpecContext) {
		uncached := inspect(ctx, opts)
		store := &countingStore{Store: cache.NewMemory()}
		opts.Analysis, opts.AnalysisTTL = store, time.Hour
		cold := inspect(ctx, opts)
		Expect([]int{store.mgets, store.sets}).To(Equal([]int{2, 3}), "one read for the script bodies, one for the module level; two script bodies and one definition analysed")

		store.mgets, store.sets = 0, 0
		warm := inspect(ctx, opts)

		Expect([]int{store.mgets, store.sets}).To(Equal([]int{2, 0}))
		Expect(warm).To(Equal(uncached))
		Expect(cold).To(Equal(uncached))
	})

	It("fails a cached batch error exactly as splitting the body does", func(ctx SpecContext) {
		store := cache.NewMemory()
		Expect(store.Set(ctx, scripts.AnalysisKey("SELECT dbo.country()", false), []byte(`{"analysis":{"calls":[]},"batchError":" has no statements"}`), 0)).To(Succeed())
		opts.Analysis = store

		_, err := Plan(ctx, opts)

		Expect(err).To(MatchError("SQL script seed has no statements"))
	})

	DescribeTable("fails loudly when the cache cannot answer",
		func(ctx SpecContext, prime func(context.Context, *countingStore), message string) {
			store := &countingStore{Store: cache.NewMemory()}
			prime(ctx, store)
			opts.Analysis = store

			_, err := Plan(ctx, opts)

			Expect(err).To(MatchError(ContainSubstring(message)))
			Expect(store.sets).To(BeZero())
		},
		Entry("a failed read", func(_ context.Context, s *countingStore) { s.mgetErr = errors.New("connection reset") }, "read script analysis cache: connection reset"),
		Entry("a corrupt value", func(ctx context.Context, s *countingStore) {
			Expect(s.Store.Set(ctx, scripts.AnalysisKey("SELECT dbo.country()", false), []byte("{"), 0)).To(Succeed())
		}, "decode cached script analysis"),
	)
})

var _ = Describe("Batched module observation", func() {
	var db Options
	var mock sqlmock.Sqlmock
	BeforeEach(func() {
		conn, m, err := sqlmock.New()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			Expect(m.ExpectationsWereMet()).To(Succeed())
			m.ExpectClose()
			Expect(conn.Close()).To(Succeed())
		})
		db, mock = Options{DB: conn, Scope: "batch"}, m
	})

	DescribeTable("issues the same queries however many scripts call the modules",
		func(ctx SpecContext, scriptCount int) {
			modules := []fakeModule{function("dbo.country", "N'ZW'"), function("dbo.currency", "N'ZWL'"), function("dbo.region", "N'SADC'")}
			names := []string{"dbo.country", "dbo.currency", "dbo.region"}
			for i := range scriptCount {
				db.Scripts = append(db.Scripts, scripts.Script{ID: fmt.Sprintf("script-%02d", i), SQL: "SELECT dbo.country(), dbo.currency(), dbo.region()", Phase: scripts.Post, Dependencies: definitionDependencies(names...)})
			}
			expectTarget(mock)
			expectLevel(mock, names, modules...)

			file, err := Plan(ctx, db)

			Expect(err).NotTo(HaveOccurred())
			Expect(file.Actions).To(HaveLen(scriptCount))
			for _, item := range file.Actions {
				Expect(item.Blocked).To(BeEmpty())
				Expect(item.State.Inputs).To(HaveKey("definition:dbo.region"))
			}
		},
		Entry("one script", 1),
		Entry("twenty-five scripts", 25),
	)

	It("blocks only the scripts that call a missing module", func(ctx SpecContext) {
		db.Scripts = []scripts.Script{
			{ID: "present", SQL: "SELECT dbo.country()", Phase: scripts.Post, Dependencies: definitionDependencies("dbo.country")},
			{ID: "absent", SQL: "SELECT dbo.retired()", Phase: scripts.Post, Dependencies: definitionDependencies("dbo.retired")},
		}
		expectTarget(mock)
		expectLevel(mock, []string{"dbo.country", "dbo.retired"}, function("dbo.country", "N'ZW'"))

		file, err := Plan(ctx, db)

		Expect(err).NotTo(HaveOccurred())
		blocked := map[string][]string{}
		for _, item := range file.Actions {
			blocked[item.Script.ID] = item.Blocked
		}
		Expect(blocked).To(Equal(map[string][]string{"present": nil, "absent": {
			"error:dbo.retired: module [dbo].[retired] not found or not visible",
		}}))
	})

	It("warns about a script calling a module that controls its own transaction or runs dynamic SQL", func(ctx SpecContext) {
		procedure := func(name, body string) fakeModule {
			m := function(name, "N'ZW'")
			m.kind, m.definition = "P", "CREATE PROCEDURE "+name+" AS "+body
			return m
		}
		db.Functions = []Function{{Name: "dbo.country", Type: "nvarchar"}}
		db.Scripts = []scripts.Script{{ID: "caller", Name: "PostDeployment_Caller", SQL: "EXEC dbo.load_codes; EXEC dbo.run_sql;", Phase: scripts.Post}}
		expectTarget(mock)
		expectLevel(mock, []string{"dbo.country", "dbo.load_codes", "dbo.run_sql"}, function("dbo.country", "N'ZW'"),
			procedure("dbo.load_codes", "BEGIN BEGIN TRANSACTION; UPDATE dbo.AsCode SET CodeValue = '1'; COMMIT; END"),
			procedure("dbo.run_sql", "BEGIN DECLARE @sql nvarchar(max) = N'SELECT 1'; EXEC(@sql); END"))

		file, err := Plan(ctx, db)

		Expect(err).NotTo(HaveOccurred())
		Expect([]any{file.Selected(), file.Actions[0].Blocked}).To(Equal([]any{[]string{"caller"}, []string(nil)}))
		Expect(file.Warnings).To(ContainElements(
			"script PostDeployment_Caller (caller) calls dbo.load_codes, which controls its own transaction inside the migration's: if it ends that transaction, or leaves one open, it is commit-unknown and blocked until reconciled",
			"script PostDeployment_Caller (caller) calls dbo.run_sql, which runs dynamic SQL: what it reads is not tracked, so a change to it does not run the script again",
		))
	})

	It("tracks DB_NAME() through a module a script calls, but not through a registered function, whose result stands for it", func(ctx SpecContext) {
		db.Functions = []Function{{Name: "dbo.country", Type: "nvarchar"}}
		db.Scripts = []scripts.Script{
			{ID: "module", SQL: "SELECT dbo.region()", Phase: scripts.Post},
			{ID: "registered", SQL: "SELECT dbo.country()", Phase: scripts.Post},
		}
		expectTarget(mock)
		expectLevel(mock, []string{"dbo.country", "dbo.region"}, function("dbo.country", "DB_NAME()"), function("dbo.region", "DB_NAME()"))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT CONVERT(nvarchar(4000), DB_NAME())")).WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("ExampleUAT"))
		expectProbe(mock, "ZW")

		file, err := Plan(ctx, db)

		Expect(err).NotTo(HaveOccurred())
		Expect([]any{file.Actions[0].Blocked, file.Actions[1].Blocked}).To(Equal([]any{[]string(nil), []string(nil)}))
		tracked := map[string][]string{}
		for _, f := range file.Functions() {
			for _, use := range f.Uses {
				tracked[f.Display()] = append(tracked[f.Display()], use.Script)
			}
		}
		Expect(tracked).To(Equal(map[string][]string{"DB_NAME()": {"module"}, "dbo.region()": {"module"}, "dbo.country()": {"registered"}}))
	})

	DescribeTable("selects the caller of a registered function by its result alone",
		func(ctx SpecContext, body, result string, dependencies []scripts.Dependency, reasons []string) {
			db.Functions = []Function{{Name: "dbo.country", Type: "nvarchar"}}
			db.Scripts = []scripts.Script{{ID: "caller", SQL: "SELECT dbo.country()", Phase: scripts.Post, Dependencies: dependencies}}
			observe := func(body, result string) {
				expectLevel(mock, []string{"dbo.country"}, function("dbo.country", body, "dbo.region"))
				expectLevel(mock, []string{"dbo.region"}, function("dbo.region", body))
				expectProbe(mock, result)
			}
			expectTarget(mock)
			observe("N'ZW'", "ZW")
			before, err := Plan(ctx, db)
			Expect(err).NotTo(HaveOccurred())
			expectIdentity(mock)
			expectLedger(mock, db.Scope, map[string]scripts.State{"caller": before.Actions[0].State})
			observe(body, result)

			file, err := Plan(ctx, db)

			Expect(err).NotTo(HaveOccurred())
			Expect(file.Actions[0].Reasons).To(Equal(reasons))
		},
		Entry("its body and callee changed, its result did not", "UPPER(N'zw')", "ZW", nil, nil),
		Entry("its result changed", "N'MW'", "MW", nil, []string{"function-result-changed"}),
		Entry("its body changed and the script declares that", "UPPER(N'zw')", "ZW", definitionDependencies("dbo.country"), []string{"function-definition-changed"}),
	)

	It("loads a reference chain one level per edge, following catalog references and definition calls", func(ctx SpecContext) {
		db.Scripts = []scripts.Script{{ID: "chain", SQL: "SELECT dbo.a()", Phase: scripts.Post, Dependencies: definitionDependencies("dbo.a")}}
		expectTarget(mock)
		expectLevel(mock, []string{"dbo.a"}, function("dbo.a", "dbo.b()", "dbo.b"))
		expectLevel(mock, []string{"dbo.b"}, function("dbo.b", "dbo.c()"))
		expectLevel(mock, []string{"dbo.c"}, function("dbo.c", "N'ZW'"))

		file, err := Plan(ctx, db)

		Expect(err).NotTo(HaveOccurred())
		Expect(file.Actions[0].Blocked).To(BeEmpty())
		Expect(file.Actions[0].State.Inputs).To(SatisfyAll(HaveKey("definition:dbo.a"), HaveKey("definition:dbo.b"), HaveKey("definition:dbo.c")))
	})
})
