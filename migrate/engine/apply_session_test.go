package engine_test

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Unit sessions", func() {
	var x *executorFixture
	var a engine.Artifact
	var request engine.ApplyRequest
	bigint := map[string]string{"items": "bigint", "orders": "bigint"}

	apply := func() (engine.Result, error) { return engine.Apply(context.Background(), x.server.open(), request) }
	plan := func() {
		a = x.plan(bigint)
		request = engine.ApplyRequest{Artifact: a, Hash: a.Hash, LockTimeout: 3 * time.Second}
		Expect(a.Units).To(HaveLen(2))
	}
	finish := func(unit int, commit bool) finishCall { return finishCall{Unit: a.Units[unit].ID, Commit: commit} }

	BeforeEach(func() {
		x = newExecutorFixture(map[string]string{"items": "int", "orders": "int"})
		plan()
	})

	It("opens each unit with its lock timeout, ID and transactional flag, and finishes it committing once applied", func() {
		_, err := apply()
		Expect(err).NotTo(HaveOccurred())
		Expect(x.f.opened).To(Equal([]engine.SessionOptions{
			{LockTimeout: 3 * time.Second, Unit: a.Units[0].ID, Transactional: true},
			{LockTimeout: 3 * time.Second, Unit: a.Units[1].ID, Transactional: true},
		}))
		Expect(x.f.finished).To(Equal([]finishCall{finish(0, true), finish(1, true)}))
	})

	It("passes a non-transactional unit's flag through and runs it on the bare connection", func() {
		x.nonTransactional["items"] = true
		plan()
		x.f.opened = nil
		_, err := apply()
		Expect(err).NotTo(HaveOccurred())
		Expect([]bool{x.f.opened[0].Transactional, x.f.opened[1].Transactional}).To(Equal([]bool{false, true}))
		Expect(x.server.logged("INSPECT")[0]).To(Equal("SESSION " + a.Units[0].ID + " lock=3s"))
	})

	DescribeTable("finishes every unit exactly once, committing only a unit whose postcondition passed",
		func(arrange func(), want func() []finishCall) {
			arrange()
			_, _ = apply() // every path but "applied" and "already applied" errors; the Finish calls are what matter here
			Expect(x.f.finished).To(Equal(want()))
		},
		Entry("applied", func() {}, func() []finishCall { return []finishCall{finish(0, true), finish(1, true)} }),
		Entry("already applied", func() { x.server.world = map[string]string{"items": "bigint", "orders": "bigint"} },
			func() []finishCall { return []finishCall{finish(0, false), finish(1, false)} }),
		Entry("drift", func() { x.server.world["items"] = "text" },
			func() []finishCall { return []finishCall{finish(0, false)} }),
		Entry("precondition failure", func() { x.server.rows["SELECT guard orders"] = true },
			func() []finishCall { return []finishCall{finish(0, true), finish(1, false)} }),
		Entry("statement failure", func() { x.server.execErr["ALTER orders"] = errors.New("denied") },
			func() []finishCall { return []finishCall{finish(0, true), finish(1, false)} }),
		Entry("postcondition failure", func() {
			x.verbs["modify"] = "NOOP"
			plan()
		}, func() []finishCall { return []finishCall{finish(0, false)} }),
	)

	It("reports commit-unknown from a failing Finish(true), finishing that unit once and running no more", func() {
		cause := errors.New("connection lost while committing")
		x.f.commitFails = cause
		result, err := apply()
		Expect(errors.Is(err, cause)).To(BeTrue())
		Expect(result.Outcome).To(Equal(engine.CommitUnknown))
		Expect(result.Units).To(HaveLen(1))
		Expect(x.f.finished).To(Equal([]finishCall{finish(0, true)}))
	})

	It("joins a failing Finish(false) to the error and turns already-applied into failed", func() {
		x.server.world = map[string]string{"items": "bigint", "orders": "bigint"}
		cause := errors.New("rollback broke")
		x.f.rollbackFails = cause
		result, err := apply()
		Expect(errors.Is(err, cause)).To(BeTrue())
		Expect(result.Outcome).To(Equal(engine.Failed))
		Expect(result.Units[0].Outcome).To(Equal(engine.Failed))
	})
})

var _ = Describe("TxSession", func() {
	var x *executorFixture
	var conn *sql.Conn
	cause := errors.New("lock timeout")
	setup := func(ctx context.Context, q engine.Querier) error {
		_, err := q.ExecContext(ctx, "SETUP")
		return err
	}
	open := func() engine.UnitSession {
		s, err := engine.TxSession(context.Background(), conn, setup)
		Expect(err).NotTo(HaveOccurred())
		return s
	}

	BeforeEach(func() {
		x = newExecutorFixture(nil)
		var err error
		conn, err = x.server.open().Conn(context.Background())
		Expect(err).NotTo(HaveOccurred())
	})
	AfterEach(func() { Expect(conn.Close()).To(Succeed()) })

	It("runs setup inside the transaction and commits on Finish(true)", func() {
		Expect(open().Finish(context.Background(), true)).To(Succeed())
		Expect(x.server.logged()).To(Equal([]string{"BEGIN", "SETUP", "COMMIT"}))
	})

	It("rolls back on Finish(false)", func() {
		Expect(open().Finish(context.Background(), false)).To(Succeed())
		Expect(x.server.logged()).To(Equal([]string{"BEGIN", "SETUP", "ROLLBACK"}))
	})

	It("rolls back and returns the error when setup fails", func() {
		x.server.execErr["SETUP"] = cause
		_, err := engine.TxSession(context.Background(), conn, setup)
		Expect(errors.Is(err, cause)).To(BeTrue())
		Expect(x.server.logged()).To(Equal([]string{"BEGIN", "SETUP", "ROLLBACK"}))
	})
})

var _ = Describe("Dialect hooks in classification", func() {
	var x *executorFixture
	var a engine.Artifact

	check := func() (map[string]engine.UnitState, error) {
		return engine.Check(context.Background(), x.server.open(), a)
	}

	Describe("SameDependent", func() {
		BeforeEach(func() {
			x = newExecutorFixture(map[string]string{"orders": "int"})
			x.withIndex()
			a = x.plan(map[string]string{"orders": "bigint"})
			x.server.world["orders"] = "bigint"
			x.server.indexes["orders.ix_ref"] = "other"
		})

		It("rejects a changed definition by default", func() {
			_, err := check()
			var partial *engine.PartialError
			Expect(errors.As(err, &partial)).To(BeTrue())
		})

		It("is the dialect's call: one that accepts the change makes the unit applied", func() {
			x.f.same = func(before, after engine.Dependent) bool { return before.Name == after.Name }
			states, err := check()
			Expect(err).NotTo(HaveOccurred())
			Expect(states).To(Equal(map[string]engine.UnitState{a.Units[0].ID: engine.UnitApplied}))
		})
	})

	Describe("Reconcile", func() {
		spelled := func(current, desired migrate.Schema) migrate.Schema {
			out := desired
			out.Tables = slices.Clone(desired.Tables)
			for i, t := range out.Tables {
				for _, live := range current.Tables {
					if live.Name == t.Name && live.Columns[0].Type == "int8" && t.Columns[0].Type == "bigint" {
						out.Tables[i].Columns = []migrate.Column{{Name: "type", Type: "int8"}}
					}
				}
			}
			return out
		}

		BeforeEach(func() {
			x = newExecutorFixture(map[string]string{"orders": "int"})
			a = x.plan(map[string]string{"orders": "bigint"})
			x.server.world["orders"] = "int8"
		})

		It("sees the live spelling as drift when nothing reconciles it", func() {
			_, err := check()
			var drift *engine.DriftError
			Expect(errors.As(err, &drift)).To(BeTrue())
		})

		It("classifies a unit applied when the dialect maps the desired spelling onto the live one, reconciling only the narrowed pair", func() {
			x.f.reconcile = spelled
			states, err := check()
			Expect(err).NotTo(HaveOccurred())
			Expect(states).To(Equal(map[string]engine.UnitState{a.Units[0].ID: engine.UnitApplied}))
			Expect(x.f.reconciled).To(HaveLen(1))
			Expect(x.f.reconciled[0][0].Tables).To(HaveLen(1))
			Expect(x.f.reconciled[0][1].Tables).To(HaveLen(1))
		})
	})
})
