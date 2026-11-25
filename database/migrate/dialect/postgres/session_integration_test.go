//go:build integration

package postgres_test

import (
	"database/sql"
	"time"

	"github.com/flanksource/sqlglot-go/database/migrate/dialect/postgres"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const migrationLock = 7561364747649

func lockTimeout(ctx SpecContext, conn *sql.Conn) string {
	var v string
	Expect(conn.QueryRowContext(ctx, "SELECT current_setting('lock_timeout')").Scan(&v)).To(Succeed())
	return v
}

func lockFree(ctx SpecContext, conn *sql.Conn) bool {
	var got bool
	Expect(conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", migrationLock).Scan(&got)).To(Succeed())
	if got {
		_, err := conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", migrationLock)
		Expect(err).NotTo(HaveOccurred())
	}
	return got
}

var _ = Describe("PostgreSQL non-transactional units", func() {
	var db *sql.DB
	BeforeEach(func(ctx SpecContext) { db = openDB(ctx) })

	conn := func(ctx SpecContext) *sql.Conn {
		c, err := db.Conn(ctx)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(c.Close)
		return c
	}

	It("holds a session lock and lock timeout for the unit, and Finish releases both", func(ctx SpecContext) {
		unit, other := conn(ctx), conn(ctx)
		before := lockTimeout(ctx, unit)
		s, err := postgres.Dialect.Open(ctx, unit, engine.SessionOptions{Unit: "hand-built", LockTimeout: 250 * time.Millisecond})
		Expect(err).NotTo(HaveOccurred())
		Expect(lockTimeout(ctx, unit)).To(Equal("250ms"))
		Expect(lockFree(ctx, other)).To(BeFalse(), "another session must not get the migration lock")

		_, err = s.Q.ExecContext(ctx, "CREATE TABLE handbuilt (id integer)")
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Finish(ctx, true)).To(Succeed())

		Expect(lockFree(ctx, other)).To(BeTrue())
		Expect(lockTimeout(ctx, unit)).To(Equal(before))
		var n int
		Expect(db.QueryRow("SELECT count(*) FROM handbuilt").Scan(&n)).To(Succeed())
	})

	It("fails with the lock error while another session holds the lock, and leaves the connection as it was", func(ctx SpecContext) {
		holder, waiter := conn(ctx), conn(ctx)
		_, err := holder.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationLock)
		Expect(err).NotTo(HaveOccurred())
		before := lockTimeout(ctx, waiter)

		_, err = postgres.Dialect.Open(ctx, waiter, engine.SessionOptions{Unit: "hand-built", LockTimeout: 50 * time.Millisecond})
		Expect(err).To(MatchError(ContainSubstring("acquire migration lock")))
		Expect(lockTimeout(ctx, waiter)).To(Equal(before))

		_, err = holder.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", migrationLock)
		Expect(err).NotTo(HaveOccurred())
	})
})
