//go:build integration

package sqlserver

import (
	"context"
	"database/sql"
	"time"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("a unit's session", Label("integration"), func() {
	lockHeldElsewhere := func(ctx context.Context) bool {
		other, err := db.Conn(ctx)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(other.Close)
		var status int
		Expect(other.QueryRowContext(ctx, `DECLARE @s int; EXEC @s = sys.sp_getapplock @Resource=N'`+planner.LockResource+`', @LockMode='Exclusive', @LockOwner='Session', @LockTimeout=0;
			IF @s >= 0 EXEC sys.sp_releaseapplock @Resource=N'`+planner.LockResource+`', @LockOwner='Session'; SELECT @s`).Scan(&status)).To(Succeed())
		return status < 0
	}
	settings := func(ctx context.Context, conn *sql.Conn) (lockTimeout int64, xactAbort bool) {
		var options int
		Expect(conn.QueryRowContext(ctx, "SELECT CAST(@@LOCK_TIMEOUT AS bigint), @@OPTIONS").Scan(&lockTimeout, &options)).To(Succeed())
		return lockTimeout, options&xactAbortOption != 0
	}

	DescribeTable("takes the migration lock and the session settings, and gives them all back on Finish",
		func(ctx SpecContext, transactional bool) {
			conn, err := db.Conn(ctx)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(conn.Close)
			s, err := Dialect{}.Open(ctx, conn, engine.SessionOptions{LockTimeout: 7 * time.Second, Transactional: transactional})
			Expect(err).NotTo(HaveOccurred())
			Expect(lockHeldElsewhere(ctx)).To(BeTrue())
			var timeout int64
			var abort bool
			Expect(s.Q.QueryRowContext(ctx, "SELECT CAST(@@LOCK_TIMEOUT AS bigint), CASE WHEN @@OPTIONS & 16384 <> 0 THEN 1 ELSE 0 END").Scan(&timeout, &abort)).To(Succeed())
			Expect([]any{timeout, abort}).To(Equal([]any{int64(7000), true}))

			Expect(s.Finish(ctx, false)).To(Succeed())
			Expect(lockHeldElsewhere(ctx)).To(BeFalse())
			timeout, abort = settings(ctx, conn)
			Expect([]any{timeout, abort}).To(Equal([]any{int64(-1), false}))
		},
		Entry("in a transaction", true),
		Entry("on a connection that cannot share a transaction", false),
	)

	It("runs a statement in a nested batch that carries the session options", func(ctx SpecContext) {
		var options int
		conn, err := db.Conn(ctx)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(conn.Close)
		Expect(Dialect{}.Exec(ctx, conn, engine.Statement{SQL: "CREATE TABLE dbo.session_probe (flags int NOT NULL)"})).To(Succeed())
		Expect(Dialect{}.Exec(ctx, conn, engine.Statement{SQL: "INSERT INTO dbo.session_probe SELECT @@OPTIONS"})).To(Succeed())
		Expect(conn.QueryRowContext(ctx, "SELECT flags FROM dbo.session_probe").Scan(&options)).To(Succeed())
		Expect(options & xactAbortOption).NotTo(BeZero())
	})
})

var _ = Describe("dependents read from the catalog", Label("integration"), func() {
	It("lists the dependents under the names the caller passed, though the server compares names loosely", func(ctx SpecContext) {
		mustExec(ctx, db, "CREATE SCHEMA depnames",
			"CREATE TABLE depnames.Widgets (Id varchar(36) NOT NULL PRIMARY KEY, Label varchar(10) NULL)",
			"CREATE INDEX IX_widgets_label ON depnames.Widgets (Label) INCLUDE (Id)")
		asked := engine.ColumnRef{Table: migrate.Name{Schema: "DEPNAMES", Name: "widgets"}, Column: "ID"}
		deps, err := Dialect{}.Dependents(ctx, db, migrate.Schema{}, []engine.ColumnRef{asked})
		Expect(err).NotTo(HaveOccurred())
		Expect(deps).To(HaveLen(2))
		for _, d := range deps {
			Expect(d.On).To(Equal([]engine.ColumnRef{asked}), d.Name)
		}
	})

	It("fails loudly for a column that does not exist", func(ctx SpecContext) {
		_, err := Dialect{}.Dependents(ctx, db, migrate.Schema{}, []engine.ColumnRef{{Table: migrate.Name{Schema: "depnames", Name: "Widgets"}, Column: "ghost"}})
		Expect(err).To(MatchError(ContainSubstring("does not exist")))
	})

	It("refuses, naming each object, what it cannot re-create faithfully", func(ctx SpecContext) {
		mustExec(ctx, db, "CREATE SCHEMA refuse",
			"CREATE TABLE refuse.t (id varchar(36) NOT NULL PRIMARY KEY, other varchar(36) NOT NULL, twice AS (id + 'x'))",
			"CREATE INDEX IX_t_off ON refuse.t (other, id)",
			"ALTER INDEX IX_t_off ON refuse.t DISABLE",
			"INSERT INTO refuse.t (id, other) VALUES ('a', 'b')",
			"ALTER TABLE refuse.t WITH NOCHECK ADD CONSTRAINT CK_t_id CHECK (id <> 'zzz')")
		mustExec(ctx, db, "CREATE VIEW refuse.v WITH SCHEMABINDING AS SELECT id FROM refuse.t")
		deps, err := Dialect{}.Dependents(ctx, db, migrate.Schema{}, []engine.ColumnRef{{Table: migrate.Name{Schema: "refuse", Name: "t"}, Column: "id"}})
		Expect(err).NotTo(HaveOccurred())
		refusals := map[string]string{}
		for _, d := range deps {
			if d.Refusal != "" {
				refusals[string(d.Kind)+" "+d.Name] = d.Refusal
			}
		}
		Expect(refusals).To(Equal(map[string]string{
			"index IX_t_off":        "index IX_t_off on [refuse].[t] is disabled",
			"check CK_t_id":         "check constraint CK_t_id on [refuse].[t] is not trusted",
			"computed-column twice": "computed column twice on [refuse].[t] uses [refuse].[t].id, and the planner cannot re-create computed columns",
			"view v":                "schema-bound view [refuse].[v] is bound to [refuse].[t].id, and the planner cannot re-create it",
		}))
	})
})
