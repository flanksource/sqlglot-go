//go:build integration

package integration_test

import (
	"context"
	"time"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	backend "github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver/sqlservertest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL Server schema observations and apply locks", func() {
	It("distinguishes schema changes, unrelated columns, and data changes", func(ctx SpecContext) {
		db := sqlservertest.OpenDisposable(ctx)
		Expect(execSQL(ctx, db, `CREATE TABLE dbo.Settings (Country nvarchar(40) NOT NULL, Other int NULL);
INSERT dbo.Settings VALUES(N'Zimbabwe', 1)`)).To(Succeed())
		opts := backend.Options{DB: db, Scope: "schema", Scripts: []scripts.Script{
			{ID: "column", Phase: scripts.Post, SQL: "SELECT 1;", Dependencies: []scripts.Dependency{{Kind: "schema-dependency", Name: "dbo.settings.country"}}},
			{ID: "table", Phase: scripts.Post, SQL: "SELECT 2;", Dependencies: []scripts.Dependency{{Kind: "schema-dependency", Name: "dbo.Settings"}}},
		}}
		Expect(apply(ctx, db, plan(ctx, opts)).Applied).To(Equal([]string{"column", "table"}))
		Expect(execSQL(ctx, db, "UPDATE dbo.Settings SET Country=N'Malawi', Other=2")).To(Succeed())
		Expect(plan(ctx, opts).Selected()).To(BeEmpty())
		Expect(execSQL(ctx, db, "ALTER TABLE dbo.Settings ADD Label nvarchar(20) NULL")).To(Succeed())
		file := plan(ctx, opts)
		Expect(file.Selected()).To(Equal([]string{"table"}))
		Expect(apply(ctx, db, file).Applied).To(Equal([]string{"table"}))
		Expect(execSQL(ctx, db, "ALTER TABLE dbo.Settings ALTER COLUMN Country nvarchar(60) NOT NULL")).To(Succeed())
		file = plan(ctx, opts)
		Expect(file.Selected()).To(Equal([]string{"column", "table"}))
		for _, item := range file.Actions {
			Expect(item.Reasons).To(ConsistOf("schema-dependency-changed"))
		}
	})

	It("waits for the scope's migration lock and refuses a plan made for another target", func(ctx SpecContext) {
		db := sqlservertest.OpenDisposable(ctx)
		opts := backend.Options{DB: db, Scope: "serialized", Scripts: []scripts.Script{{ID: "probe", SQL: "SELECT 1;", Phase: scripts.Post}}}
		file := plan(ctx, opts)
		holder, err := db.Conn(ctx)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(holder.Close)
		_, err = holder.ExecContext(ctx, `DECLARE @r int; EXEC @r=sys.sp_getapplock @Resource=@p1,@LockMode='Exclusive',@LockOwner='Session'`, "sqlglot/scripts:"+scripts.Digest(opts.Scope))
		Expect(err).NotTo(HaveOccurred())
		wait, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		_, err = backend.Apply(wait, db, file)
		Expect(err).To(MatchError(ContainSubstring("acquire migration lock")))
		_, err = holder.ExecContext(ctx, `EXEC sys.sp_releaseapplock @Resource=@p1,@LockOwner='Session'`, "sqlglot/scripts:"+scripts.Digest(opts.Scope))
		Expect(err).NotTo(HaveOccurred())
		Expect(apply(ctx, db, file).Applied).To(Equal([]string{"probe"}))

		separate := sqlservertest.OpenDisposable(ctx)
		_, err = backend.Apply(ctx, separate, file)
		Expect(err).To(MatchError(ContainSubstring("plan targets")))
		var ledgers int
		Expect(separate.QueryRowContext(ctx, "SELECT COUNT(*) FROM sys.tables WHERE name=N'sqlglot_migrations'").Scan(&ledgers)).To(Succeed())
		Expect(ledgers).To(BeZero())
	})
})
