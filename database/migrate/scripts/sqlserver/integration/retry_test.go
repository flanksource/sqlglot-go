//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	backend "github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver/sqlservertest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Transactional retry and cancellation", func() {
	It("retries lock timeouts without duplicating writes and preserves cancelled outcomes", func(ctx SpecContext) {
		db := sqlservertest.OpenDisposable(ctx)
		Expect(execSQL(ctx, db, `CREATE TABLE dbo.LockGate (Id int PRIMARY KEY);
INSERT dbo.LockGate VALUES (1);
CREATE TABLE dbo.RunResult (Attempt int NOT NULL);
CREATE SEQUENCE dbo.AttemptSequence START WITH 1 INCREMENT BY 1;`)).To(Succeed())
		opts := backend.Options{DB: db, Scope: "live-retry", Scripts: []scripts.Script{{ID: "writer", Phase: scripts.Post, SQL: `
SET LOCK_TIMEOUT 250;
INSERT dbo.RunResult VALUES(NEXT VALUE FOR dbo.AttemptSequence);
UPDATE dbo.LockGate SET Id = 1 WHERE Id = 1;`}}}
		file := plan(ctx, opts)

		lock := lockFixtureRow(ctx, db)
		outcomes := beginApply(ctx, db, file)
		Eventually(func() int { return lastAttempt(ctx, db) }).WithTimeout(6 * time.Second).Should(BeNumerically(">=", 2))
		Expect(lock.Rollback()).To(Succeed())
		var retry applyOutcome
		Eventually(outcomes).WithTimeout(6 * time.Second).Should(Receive(&retry))
		Expect(retry.err).NotTo(HaveOccurred())
		Expect(retry.result.Applied).To(Equal([]string{"writer"}))

		// A separate migration scope selects the same body again, this time cancelling while it is blocked.
		opts.Scope = "live-cancel"
		file = plan(ctx, opts)
		priorAttempt := lastAttempt(ctx, db)
		lock = lockFixtureRow(ctx, db)
		cancelled, cancel := context.WithCancel(ctx)
		defer cancel()
		outcomes = beginApply(cancelled, db, file)
		Eventually(func() int { return lastAttempt(ctx, db) }).WithTimeout(6 * time.Second).Should(BeNumerically(">", priorAttempt))
		cancel()
		var cancelledOutcome applyOutcome
		Eventually(outcomes).WithTimeout(6 * time.Second).Should(Receive(&cancelledOutcome))
		Expect(cancelledOutcome.err).To(HaveOccurred())
		Expect(cancelledOutcome.result.Outcome).To(BeElementOf("failed", "commit-unknown"))
		Expect(lock.Rollback()).To(Succeed())
		var count int
		Expect(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dbo.RunResult").Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))
		after := plan(ctx, opts)
		if cancelledOutcome.result.Outcome == "commit-unknown" {
			Expect(after.Actions[0].Blocked).To(ContainElement(ContainSubstring("reconciliation required")))
		} else {
			Expect(after.Actions[0].Reasons).To(ContainElement("retry-failed"))
		}
	})
})

type applyOutcome struct {
	result backend.Result
	err    error
}

func beginApply(ctx context.Context, db *sql.DB, file backend.PlanFile) <-chan applyOutcome {
	outcomes := make(chan applyOutcome, 1)
	go func() {
		result, err := backend.Apply(ctx, db, file)
		outcomes <- applyOutcome{result: result, err: err}
	}()
	return outcomes
}

func lockFixtureRow(ctx context.Context, db *sql.DB) *sql.Tx {
	GinkgoHelper()
	tx, err := db.BeginTx(ctx, nil)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() {
		if err := tx.Rollback(); !errors.Is(err, sql.ErrTxDone) {
			Expect(err).NotTo(HaveOccurred())
		}
	})
	_, err = tx.ExecContext(ctx, "UPDATE dbo.LockGate SET Id=1 WHERE Id=1")
	Expect(err).NotTo(HaveOccurred())
	return tx
}

func lastAttempt(ctx context.Context, db *sql.DB) int {
	GinkgoHelper()
	var attempt int
	Expect(db.QueryRowContext(ctx, "SELECT COALESCE(CONVERT(int,last_used_value),0) FROM sys.sequences WHERE name=N'AttemptSequence'").Scan(&attempt)).To(Succeed())
	return attempt
}
