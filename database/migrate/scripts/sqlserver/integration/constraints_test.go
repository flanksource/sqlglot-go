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

var _ = Describe("SQL Server schema observations and review locks", func() {
	It("distinguishes schema changes, unrelated columns, and data changes", func(ctx SpecContext) {
		db := sqlservertest.OpenDisposable(ctx)
		Expect(execSQL(ctx, db, `CREATE TABLE dbo.Settings (Country nvarchar(40) NOT NULL, Other int NULL);
INSERT dbo.Settings VALUES(N'Zimbabwe', 1)`)).To(Succeed())
		opts := backend.Options{DB: db, Scope: "schema", Scripts: []scripts.Script{
			{ID: "column", Phase: scripts.Post, SQL: "SELECT 1;", Dependencies: []scripts.Dependency{{Kind: "schema-dependency", Name: "dbo.settings.country"}}},
			{ID: "table", Phase: scripts.Post, SQL: "SELECT 2;", Dependencies: []scripts.Dependency{{Kind: "schema-dependency", Name: "dbo.Settings"}}},
		}}
		session, err := backend.Open(ctx, opts)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(session.Close)
		inspect := func() backend.Preview {
			plan, err := session.Inspect(ctx, scripts.Post)
			Expect(err).NotTo(HaveOccurred())
			return plan
		}
		Expect(apply(ctx, session, inspect()).Applied).To(Equal([]string{"column", "table"}))
		Expect(execSQL(ctx, db, "UPDATE dbo.Settings SET Country=N'Malawi', Other=2")).To(Succeed())
		Expect(selectedIDs(inspect())).To(BeEmpty())
		Expect(execSQL(ctx, db, "ALTER TABLE dbo.Settings ADD Label nvarchar(20) NULL")).To(Succeed())
		plan := inspect()
		Expect(selectedIDs(plan)).To(Equal([]string{"table"}))
		Expect(apply(ctx, session, plan).Applied).To(Equal([]string{"table"}))
		Expect(execSQL(ctx, db, "ALTER TABLE dbo.Settings ALTER COLUMN Country nvarchar(60) NOT NULL")).To(Succeed())
		plan = inspect()
		Expect(selectedIDs(plan)).To(Equal([]string{"column", "table"}))
		for _, item := range plan.Plan.Scripts {
			Expect(item.Reasons).To(ConsistOf("schema-dependency-changed"))
		}
		AddReportEntry("C17", map[string]any{"dataOnly": []string{}, "otherColumn": []string{"table"}, "declaredColumn": selectedIDs(plan)})
	})

	It("waits for the owning session to release the bundle lock", func(ctx SpecContext) {
		db := sqlservertest.OpenDisposable(ctx)
		opts := backend.Options{DB: db, Scope: "serialized", Scripts: []scripts.Script{{ID: "probe", SQL: "SELECT 1;", Phase: scripts.Post}}}
		first, err := backend.Open(ctx, opts)
		Expect(err).NotTo(HaveOccurred())
		held := true
		DeferCleanup(func() {
			if held {
				Expect(first.Close()).To(Succeed())
			}
		})
		wait, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		other, err := backend.Open(wait, opts)
		Expect(err).To(HaveOccurred())
		Expect(other).To(BeNil())
		original, err := first.Inspect(ctx, scripts.Post)
		Expect(err).NotTo(HaveOccurred())
		Expect(apply(ctx, first, original).Applied).To(Equal([]string{"probe"}))
		separate := opts
		separate.DB = sqlservertest.OpenDisposable(ctx)
		secondTarget, err := backend.Inspect(ctx, separate, scripts.Post)
		Expect(err).NotTo(HaveOccurred())
		Expect(secondTarget.Plan.Target).NotTo(Equal(original.Plan.Target))
		Expect(selectedIDs(secondTarget)).To(Equal([]string{"probe"}))
		secondSession, err := backend.Open(ctx, separate)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(secondSession.Close)
		stale := apply(ctx, secondSession, original)
		Expect(stale.Outcome).To(Equal("continuation-required"))
		Expect(stale.Applied).To(BeEmpty())
		var ledgers int
		Expect(separate.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM sys.tables WHERE name=N'sqlglot_migrations'").Scan(&ledgers)).To(Succeed())
		Expect(ledgers).To(BeZero())
		Expect(first.Close()).To(Succeed())
		held = false
		second, err := backend.Open(ctx, opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(second.Close()).To(Succeed())
		AddReportEntry("C12", map[string]any{"secondConnectionWaited": true, "otherTargetSelection": selectedIDs(secondTarget)})
		AddReportEntry("target-binding", map[string]any{"outcome": stale.Outcome, "createdLedgers": ledgers})
	})
})
