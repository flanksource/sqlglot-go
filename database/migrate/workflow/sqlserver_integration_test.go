//go:build integration

package workflow

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/flanksource/sqlglot-go/database/migrate/dialect"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver/sqlservertest"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// execer is a database or a connection.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func execAll(ctx context.Context, db execer, statements ...string) {
	GinkgoHelper()
	for _, s := range statements {
		_, err := db.ExecContext(ctx, s)
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), s)
	}
}

func scalarString(ctx context.Context, db execer, query string) string {
	GinkgoHelper()
	var v string
	ExpectWithOffset(1, db.QueryRowContext(ctx, query).Scan(&v)).To(Succeed(), query)
	return v
}

const (
	guidA = "0A1B2C3D-0000-4000-8000-00000000AAAA"
	guidB = "0A1B2C3D-0000-4000-8000-00000000BBBB"
	// refType reads the type name of the retyped column.
	refType = "SELECT t.name FROM sys.columns c JOIN sys.types t ON t.user_type_id = c.user_type_id WHERE c.object_id = OBJECT_ID('wf.accounts') AND c.name = 'ref'"
)

// accountsDDL is the starting schema, every constraint named so two databases hold it identically.
var accountsDDL = []string{
	"CREATE SCHEMA wf",
	"CREATE TABLE wf.accounts (id int NOT NULL CONSTRAINT PK_accounts PRIMARY KEY, ref varchar(36) NOT NULL, note nvarchar(50) NULL)",
	"CREATE INDEX IX_accounts_ref ON wf.accounts (ref) INCLUDE (note)",
}

// onDatabase runs fn on a connection switched to another database of the server, and switches it back.
func onDatabase(ctx context.Context, db *sql.DB, name string, fn func(conn *sql.Conn)) {
	GinkgoHelper()
	conn, err := db.Conn(ctx)
	Expect(err).NotTo(HaveOccurred())
	home := scalarString(ctx, conn, "SELECT DB_NAME()")
	execAll(ctx, conn, "USE ["+name+"]")
	defer func() {
		execAll(ctx, conn, "USE ["+home+"]")
		Expect(conn.Close()).To(Succeed())
	}()
	fn(conn)
}

var _ = Describe("a SQL Server GUID retype through the workflow", Ordered, Label("integration"), func() {
	var (
		db       *sql.DB
		plan     PlanResult
		artifact engine.Artifact
		script   string
		final    dialect.Snapshot
	)

	BeforeAll(func(ctx SpecContext) {
		db = sqlservertest.OpenDisposable(ctx)
		execAll(ctx, db, "ALTER DATABASE CURRENT SET COMPATIBILITY_LEVEL = 100", "CREATE DATABASE wf_copy", "CREATE DATABASE wf_bad", "CREATE DATABASE wf_default")
		Expect(scalarString(ctx, db, "SELECT CAST(compatibility_level AS varchar(10)) FROM sys.databases WHERE name = DB_NAME()")).To(Equal("100"))
		execAll(ctx, db, accountsDDL...)
		execAll(ctx, db, "INSERT INTO wf.accounts VALUES (1, '"+guidA+"', 'a'), (2, '"+guidB+"', NULL)")
	})

	It("plans the retype of an indexed column: the index is rebuilt around it and the data is checked first", func(ctx SpecContext) {
		desired, err := dialect.Inspect(ctx, db, "sqlserver", []string{"wf"})
		Expect(err).NotTo(HaveOccurred())
		for i := range desired.Schema.Tables[0].Columns {
			if c := &desired.Schema.Tables[0].Columns[i]; c.Name == "ref" {
				c.Type, c.Collation = "uniqueidentifier", ""
			}
		}
		content, err := json.Marshal(desired)
		Expect(err).NotTo(HaveOccurred())

		plan, err = Plan(ctx, db, Options{Dialect: "sqlserver", Schema: "wf", Source: Source{Kind: "snapshot", Content: string(content)}})
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal(plan.Artifact, &artifact)).To(Succeed())

		modify := `["modify","column","wf","accounts","ref"]`
		Expect(plan.Scope.Mode).To(Equal(engine.ScopeComplete))
		Expect(plan.Changes).To(HaveLen(1))
		Expect(plan.Changes[0].ID).To(Equal(modify))
		Expect(plan.Changes[0].Rebuilds).To(HaveLen(1))
		Expect(plan.Changes[0].Rebuilds[0].Member).To(Equal("IX_accounts_ref"))
		Expect(plan.Changes[0].Rebuilds[0].Cause).To(Equal(modify))
		Expect(plan.Units).To(HaveLen(1))
		Expect(plan.Units[0].Preconditions).To(HaveLen(1))
		Expect(plan.Units[0].Preconditions[0].ID).To(Equal(modify + "#guid"))
		Expect(plan.Acks).To(Equal([]string{modify}))
	})

	It("renders the plan as a script that guards each unit and throws the precondition's message", func() {
		var err error
		script, err = planner.RenderScript(artifact, planner.ScriptOptions{Acknowledgements: plan.Acks})
		Expect(err).NotTo(HaveOccurred())
		Expect(script).To(ContainSubstring("THROW 50000, N'[wf].[accounts].ref holds values that are not canonical GUIDs"))
		Expect(script).To(ContainSubstring("matches neither its baseline nor its desired state"))
	})

	It("applies the plan: the column is a uniqueidentifier, its index is back and no row changed", func(ctx SpecContext) {
		result, err := Apply(ctx, db, ApplyOptions{Dialect: "sqlserver", Schema: "wf", Artifact: plan.Artifact, Acks: plan.Acks})
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(HaveField("Outcome", engine.Applied))
		Expect(scalarString(ctx, db, refType)).To(Equal("uniqueidentifier"))
		Expect(scalarString(ctx, db, "SELECT name FROM sys.indexes WHERE object_id = OBJECT_ID('wf.accounts') AND name = 'IX_accounts_ref'")).To(Equal("IX_accounts_ref"))
		Expect(scalarString(ctx, db, "SELECT CAST(ref AS varchar(36)) FROM wf.accounts WHERE id = 1")).To(Equal(guidA))

		again, err := Apply(ctx, db, ApplyOptions{Dialect: "sqlserver", Schema: "wf", Artifact: plan.Artifact, Acks: plan.Acks})
		Expect(err).NotTo(HaveOccurred())
		Expect(again).To(HaveField("Outcome", engine.AlreadyApplied))
		final, err = dialect.Inspect(ctx, db, "sqlserver", []string{"wf"})
		Expect(err).NotTo(HaveOccurred())
	})

	It("reaches the same final schema when the rendered script runs on a second copy, running it again changes nothing, and the session options are back", func(ctx SpecContext) {
		onDatabase(ctx, db, "wf_copy", func(conn *sql.Conn) {
			execAll(ctx, conn, "ALTER DATABASE CURRENT SET COMPATIBILITY_LEVEL = 100")
			execAll(ctx, conn, accountsDDL...)
			execAll(ctx, conn, "INSERT INTO wf.accounts VALUES (1, '"+guidA+"', 'a'), (2, '"+guidB+"', NULL)")
			execAll(ctx, conn, "SET XACT_ABORT OFF")

			execAll(ctx, conn, script)
			Expect(scalarString(ctx, conn, refType)).To(Equal("uniqueidentifier"))
			got, err := dialect.Inspect(ctx, conn, "sqlserver", []string{"wf"})
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Schema).To(Equal(final.Schema))

			execAll(ctx, conn, script)
			again, err := dialect.Inspect(ctx, conn, "sqlserver", []string{"wf"})
			Expect(err).NotTo(HaveOccurred())
			Expect(again.Schema).To(Equal(final.Schema))
			Expect(scalarString(ctx, conn, "SELECT CAST(@@OPTIONS & 16384 AS varchar(10))")).To(Equal("0"), "XACT_ABORT is restored to what the connection had")
		})
	})

	It("drops a column default under the name the target gives it, and adds the desired one back", func(ctx SpecContext) {
		ddl := []string{"CREATE SCHEMA wd", "CREATE TABLE wd.defaulted (id int NOT NULL CONSTRAINT PK_defaulted PRIMARY KEY, label nvarchar(20) NOT NULL CONSTRAINT DF_defaulted_label DEFAULT (N'open'))"}
		execAll(ctx, db, ddl...)
		desired, err := dialect.Inspect(ctx, db, "sqlserver", []string{"wd"})
		Expect(err).NotTo(HaveOccurred())
		for i := range desired.Schema.Tables[0].Columns {
			if c := &desired.Schema.Tables[0].Columns[i]; c.Name == "label" {
				c.Type = "nvarchar(100)"
			}
		}
		content, err := json.Marshal(desired)
		Expect(err).NotTo(HaveOccurred())
		defaulted, err := Plan(ctx, db, Options{Dialect: "sqlserver", Schema: "wd", Source: Source{Kind: "snapshot", Content: string(content)}})
		Expect(err).NotTo(HaveOccurred())
		var a engine.Artifact
		Expect(json.Unmarshal(defaulted.Artifact, &a)).To(Succeed())
		rendered, err := planner.RenderScript(a, planner.ScriptOptions{Acknowledgements: defaulted.Acks})
		Expect(err).NotTo(HaveOccurred())

		onDatabase(ctx, db, "wf_default", func(conn *sql.Conn) {
			execAll(ctx, conn, "ALTER DATABASE CURRENT SET COMPATIBILITY_LEVEL = 100")
			execAll(ctx, conn, ddl...)
			execAll(ctx, conn, "EXEC sp_rename N'wd.DF_defaulted_label', N'DF_elsewhere', N'OBJECT'")

			execAll(ctx, conn, rendered)
			Expect(scalarString(ctx, conn, "SELECT CAST(max_length AS varchar(10)) FROM sys.columns WHERE object_id = OBJECT_ID('wd.defaulted') AND name = 'label'")).To(Equal("200"))
			Expect(scalarString(ctx, conn, "SELECT name FROM sys.default_constraints WHERE parent_object_id = OBJECT_ID('wd.defaulted')")).To(Equal("DF_defaulted_label"))
		})
	})

	It("stops the script on a value that is not a GUID, with the column and its index untouched", func(ctx SpecContext) {
		onDatabase(ctx, db, "wf_bad", func(conn *sql.Conn) {
			execAll(ctx, conn, "ALTER DATABASE CURRENT SET COMPATIBILITY_LEVEL = 100")
			execAll(ctx, conn, accountsDDL...)
			execAll(ctx, conn, "INSERT INTO wf.accounts VALUES (1, '"+guidA+"', 'a'), (2, 'not-a-guid', NULL)")

			_, err := conn.ExecContext(ctx, script)
			Expect(err).To(MatchError(ContainSubstring("[wf].[accounts].ref holds values that are not canonical GUIDs")))
			Expect(scalarString(ctx, conn, refType)).To(Equal("varchar"))
			Expect(scalarString(ctx, conn, "SELECT name FROM sys.indexes WHERE object_id = OBJECT_ID('wf.accounts') AND name = 'IX_accounts_ref'")).To(Equal("IX_accounts_ref"))
		})
	})
})
