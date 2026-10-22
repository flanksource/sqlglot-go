package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"

	"ariga.io/atlas/sql/schema"
	sqlitemigrate "github.com/flanksource/sqlglot-go/database/migrate/sqlite"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQLite drop policy", func() {
	var database *sql.DB
	var ctx context.Context
	BeforeEach(func() {
		ctx = context.Background()
		var err error
		database, err = sql.Open("sqlite", filepath.Join(GinkgoT().TempDir(), "policy.db"))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(database.Close)
		_, err = database.ExecContext(ctx, eventsTable)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.ExecContext(ctx, `CREATE TABLE other (value TEXT); INSERT INTO other VALUES ('retained')`)
		Expect(err).NotTo(HaveOccurred())
	})

	DescribeTable("preserves omitted data through a rebuild unless drops are enabled", func(allowDrops bool) {
		declared := declaredEvents()
		declared.Columns = declared.Columns[:3]
		declared.AddChecks(&schema.Check{Name: "events_seq_positive", Expr: "seq > 0"})
		options := sqlitemigrate.ReconcileOptions{AllowDrops: allowDrops}
		for range 2 {
			Expect(sqlitemigrate.ReconcileTables(ctx, database, options, declared)).To(Succeed())
		}
		var noteColumns, otherTables int
		Expect(database.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('events') WHERE name = 'note'`).Scan(&noteColumns)).To(Succeed())
		Expect(database.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'other'`).Scan(&otherTables)).To(Succeed())
		if allowDrops {
			Expect([]int{noteColumns, otherTables}).To(Equal([]int{0, 0}))
		} else {
			Expect([]int{noteColumns, otherTables}).To(Equal([]int{1, 1}))
			var note, other string
			Expect(database.QueryRowContext(ctx, `SELECT note FROM events WHERE seq = 1`).Scan(&note)).To(Succeed())
			Expect(database.QueryRowContext(ctx, `SELECT value FROM other`).Scan(&other)).To(Succeed())
			Expect([]string{note, other}).To(Equal([]string{"first", "retained"}))
		}
		Expect(declared.Columns).NotTo(ContainElement(HaveField("Name", "note")))
		pending, err := sqlitemigrate.PendingChanges(ctx, database, options, declared)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(BeEmpty())
		_, err = database.ExecContext(ctx, `INSERT INTO events (stream_id, seq, name) VALUES ('run-2', 0, 'invalid')`)
		Expect(err).To(MatchError(ContainSubstring("CHECK constraint failed")))
	}, Entry("default", false), Entry("explicit opt-in", true))

	It("reports no pending work when only a column and table are omitted", func() {
		declared := declaredEvents()
		declared.Columns = declared.Columns[:3]
		pending, err := sqlitemigrate.PendingChanges(ctx, database, sqlitemigrate.ReconcileOptions{}, declared)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(BeEmpty())
	})

	It("allows an explicitly empty desired schema to remove all user tables only with drop opt-in", func() {
		Expect(sqlitemigrate.ReconcileTables(ctx, database, sqlitemigrate.ReconcileOptions{})).To(MatchError(ContainSubstring("no table")))
		Expect(sqlitemigrate.ReconcileTables(ctx, database, sqlitemigrate.ReconcileOptions{AllowDrops: true})).To(Succeed())
		var count int
		Expect(database.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&count)).To(Succeed())
		Expect(count).To(BeZero())
	})

	It("widens a unique key with a new Git path without enabling drops", func() {
		declared := declaredEvents()
		gitPath := column("git_path", "TEXT")
		gitPath.Type.Null = false
		gitPath.Default = &schema.Literal{V: "''"}
		declared.AddColumns(gitPath)
		declared.Indexes[0].AddColumns(gitPath)
		Expect(sqlitemigrate.ReconcileTables(ctx, database, sqlitemigrate.ReconcileOptions{}, declared)).To(Succeed())
		var note, path string
		Expect(database.QueryRowContext(ctx, `SELECT note, git_path FROM events WHERE seq = 1`).Scan(&note, &path)).To(Succeed())
		Expect([]string{note, path}).To(Equal([]string{"first", ""}))
		_, err := database.ExecContext(ctx, `INSERT INTO events (stream_id, seq, name, git_path) VALUES ('run-1', 2, 'a', 'other.go')`)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.ExecContext(ctx, `INSERT INTO events (stream_id, seq, name, git_path) VALUES ('run-1', 3, 'a', 'other.go')`)
		Expect(err).To(MatchError(ContainSubstring("UNIQUE constraint failed")))
	})
})
