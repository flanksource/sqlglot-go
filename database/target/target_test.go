package dbtarget_test

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/sqlglot-go/database/target"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	_ "modernc.org/sqlite"
)

var _ = Describe("database target DSNs", func() {
	DescribeTable("classifies PostgreSQL without rewriting its DSN",
		func(dsn string) {
			target, err := dbtarget.Parse(dsn)
			Expect(err).ToNot(HaveOccurred())
			Expect(target).To(Equal(dbtarget.Target{Dialect: dbtarget.Postgres, DSN: dsn}))
		},
		Entry("postgres URL", "postgres://localhost/app?sslmode=disable"),
		Entry("postgresql URL", "postgresql://localhost/app?sslmode=disable"),
		Entry("keyword DSN whose database ends in db", "host=localhost dbname=example.db sslmode=disable"),
	)

	DescribeTable("classifies SQL Server without rewriting its DSN",
		func(dsn string) {
			target, err := dbtarget.Parse(dsn)
			Expect(err).ToNot(HaveOccurred())
			Expect(target).To(Equal(dbtarget.Target{Dialect: dbtarget.SQLServer, DSN: dsn}))
		},
		Entry("sqlserver URL with database query", "sqlserver://sa:secret@localhost:1433?database=oipa"),
		Entry("upper-case scheme", "SQLServer://sa:secret@localhost?database=oipa"),
		Entry("credentials with escaped special characters", "sqlserver://app%40corp:p%40ss%3Aw%2Frd%23@db.example:1433/instance?database=x&encrypt=disable"),
		Entry("ADO keywords", "server=db.example;user id=app;password=secret;database=acme;encrypt=disable"),
		Entry("ADO data source with initial catalog", "Data Source=db.example,1433;Initial Catalog=acme;User ID=app;Password=secret"),
		Entry("ADO mixed-case keys and a trailing separator", "Server = db.example ; Database=acme;"),
		Entry("ADO address key", "addr=db.example;database=acme"),
		Entry("ODBC prefix", "odbc:server=db.example;user id=app;password={se;cret};database=acme"),
	)

	It("keeps a space-separated keyword DSN on PostgreSQL even when it names a server key", func() {
		target, err := dbtarget.Parse("host=localhost dbname=acme server=ignored")
		Expect(err).ToNot(HaveOccurred())
		Expect(target.Dialect).To(Equal(dbtarget.Postgres))
	})

	DescribeTable("classifies and normalizes SQLite",
		func(dsn, expectedPath string) {
			target, err := dbtarget.Parse(dsn)
			Expect(err).ToNot(HaveOccurred())
			Expect(target.Dialect).To(Equal(dbtarget.SQLite))

			parsed, err := url.Parse(target.DSN)
			Expect(err).ToNot(HaveOccurred())
			Expect(parsed.Scheme).To(Equal("file"))
			Expect(parsed.Host).To(BeEmpty())
			path := parsed.Path
			if filepath.VolumeName(expectedPath) != "" {
				path = strings.TrimPrefix(path, "/")
			}
			Expect(filepath.FromSlash(path)).To(Equal(expectedPath))
			Expect(parsed.Query()["_pragma"]).To(ConsistOf("foreign_keys(1)", "busy_timeout(5000)", "journal_mode(WAL)"))
		},
		Entry("absolute sqlite URL", "sqlite:///var/data/uir.db", absolutePath("/var/data/uir.db")),
		Entry("explicit relative sqlite URL with any extension", "sqlite://state/uir.sqlite", absolutePath("state/uir.sqlite")),
		Entry("bare relative db path", "state/uir.db", absolutePath("state/uir.db")),
	)

	It("opens the exact native filename with spaces and URL punctuation", func(ctx SpecContext) {
		filename := filepath.Join(GinkgoT().TempDir(), "tenant #1.db")
		target, err := dbtarget.Parse(filename)
		Expect(err).NotTo(HaveOccurred())
		db, err := sql.Open("sqlite", target.DSN)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		_, err = db.ExecContext(ctx, "CREATE TABLE items (id integer)")
		Expect(err).NotTo(HaveOccurred())
		info, err := os.Stat(filename)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.IsDir()).To(BeFalse())
	})

	It("preserves SQLite query parameters and replaces managed pragmas", func() {
		target, err := dbtarget.Parse("sqlite://state/uir.db?cache=shared&_pragma=foreign_keys(0)&_pragma=case_sensitive_like(1)")
		Expect(err).ToNot(HaveOccurred())
		parsed, err := url.Parse(target.DSN)
		Expect(err).ToNot(HaveOccurred())
		Expect(parsed.Query().Get("cache")).To(Equal("shared"))
		Expect(parsed.Query()["_pragma"]).To(ConsistOf(
			"case_sensitive_like(1)", "foreign_keys(1)", "busy_timeout(5000)", "journal_mode(WAL)",
		))
	})

	DescribeTable("rejects invalid or unsupported DSNs",
		func(dsn, message string) {
			_, err := dbtarget.Parse(dsn)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("empty", " ", "DSN is required"),
		Entry("unknown URL scheme", "mysql://localhost/app", `unsupported database scheme "mysql"`),
		Entry("mssql alias scheme", "mssql://sa:secret@localhost?database=oipa", `use sqlserver://`),
		Entry("explicit memory database", "sqlite://:memory:", "file-backed"),
		Entry("file URI memory database", "sqlite://file::memory:?cache=shared", "file-backed"),
		Entry("upper-case file URI scheme memory database", "sqlite://FILE::memory:", "file-backed"),
		Entry("percent-encoded file URI memory database", "sqlite://file:%3Amemory%3A", "file-backed"),
		Entry("partially percent-encoded file URI memory database", "sqlite://file::memory%3a?cache=shared", "file-backed"),
		Entry("malformed file URI escape", "sqlite://file:state%zz.db", "decode SQLite file URI"),
		Entry("bare memory database", ":memory:", "sqlite://"),
	)

	It("treats a file URI whose filename only case-folds to :memory: as a disk file", func() {
		target, err := dbtarget.Parse("sqlite://file::MEMORY:")
		Expect(err).ToNot(HaveOccurred())
		Expect(target.Dialect).To(Equal(dbtarget.SQLite))
		parsed, err := url.Parse(target.DSN)
		Expect(err).ToNot(HaveOccurred())
		Expect(parsed.Opaque).To(Equal(":MEMORY:"))
	})
})

func TestDatabaseTarget(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Database Target Suite")
}

func absolutePath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		panic(err)
	}
	return filepath.Clean(strings.TrimSpace(absolute))
}
