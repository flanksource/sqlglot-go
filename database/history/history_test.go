package history_test

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	clickytask "github.com/flanksource/clicky/task"
	"github.com/flanksource/sqlglot-go/database/history"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestHistory(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Run history")
}

func openStore() *history.Store {
	store, err := history.Open(filepath.Join(GinkgoT().TempDir(), "nested", "history.db"))
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(store.Close)
	return store
}

var _ = Describe("History records", func() {
	var (
		store   *history.Store
		created = time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC)
	)
	BeforeEach(func() { store = openStore() })

	comparison := func(id string, at time.Time) history.Record {
		return history.Record{ID: id, Kind: history.KindComparison, Dialect: "sqlite", Reference: "connection://acme/prod", Target: "connection://acme/staging",
			Schemas: []string{"main"}, Status: history.StatusSucceeded, CreatedAt: at, Summary: map[string]int{"changes": 2}, Payload: json.RawMessage(`{"changes":[]}`)}
	}

	It("returns a saved record with its payload, and lists it newest first without one", func() {
		older, newer := comparison("run-1", created), comparison("run-2", created.Add(time.Minute))
		Expect(store.Save(GinkgoT().Context(), older)).To(Succeed())
		Expect(store.Save(GinkgoT().Context(), newer)).To(Succeed())

		got, err := store.Get(GinkgoT().Context(), "run-1")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(older))

		listed, err := store.List(GinkgoT().Context(), history.Filter{})
		Expect(err).NotTo(HaveOccurred())
		newer.Payload, older.Payload = nil, nil
		Expect(listed).To(Equal([]history.Record{newer, older}))
	})

	It("filters by kind and by a connection on either side", func() {
		inspection := history.Record{ID: "run-1/target", Kind: history.KindInspection, Parent: "run-1", Dialect: "sqlite", Target: "connection://acme/staging",
			Schemas: []string{"main"}, Status: history.StatusSucceeded, CreatedAt: created, Summary: map[string]int{}, Payload: json.RawMessage(`{}`)}
		other := comparison("run-2", created)
		other.Reference, other.Target = "connection://acme/dev", "connection://acme/qa"
		for _, r := range []history.Record{comparison("run-1", created), inspection, other} {
			Expect(store.Save(GinkgoT().Context(), r)).To(Succeed())
		}
		ids := func(filter history.Filter) []string {
			listed, err := store.List(GinkgoT().Context(), filter)
			Expect(err).NotTo(HaveOccurred())
			out := []string{}
			for _, r := range listed {
				out = append(out, r.ID)
			}
			return out
		}
		Expect(ids(history.Filter{Kind: history.KindInspection})).To(Equal([]string{"run-1/target"}))
		Expect(ids(history.Filter{Connection: "connection://acme/staging"})).To(ConsistOf("run-1", "run-1/target"))
		Expect(ids(history.Filter{Connection: "connection://acme/dev"})).To(Equal([]string{"run-2"}))
	})

	It("refuses a second record with the same id and an unknown id", func() {
		Expect(store.Save(GinkgoT().Context(), comparison("run-1", created))).To(Succeed())
		Expect(store.Save(GinkgoT().Context(), comparison("run-1", created))).To(MatchError(ContainSubstring("history record run-1 already exists")))
		_, err := store.Get(GinkgoT().Context(), "missing")
		Expect(err).To(MatchError(history.ErrNotFound))
		Expect(store.Has(GinkgoT().Context(), "run-1")).To(BeTrue())
		Expect(store.Has(GinkgoT().Context(), "missing")).To(BeFalse())
	})

	It("rejects a record without an id, kind or status", func() {
		Expect(store.Save(GinkgoT().Context(), history.Record{Kind: history.KindPatch, Status: history.StatusFailed, CreatedAt: created})).To(MatchError(ContainSubstring("id")))
		Expect(store.Save(GinkgoT().Context(), history.Record{ID: "x", Status: history.StatusFailed, CreatedAt: created})).To(MatchError(ContainSubstring("kind")))
		Expect(store.Save(GinkgoT().Context(), history.Record{ID: "x", Kind: history.KindPatch, CreatedAt: created})).To(MatchError(ContainSubstring("status")))
	})
})

var _ = DescribeTable("Redacting a connection before it is recorded",
	func(dsn, expected string) { Expect(history.Redact(dsn)).To(Equal(expected)) },
	Entry("named connection is kept whole", "connection://acme/prod", "connection://acme/prod"),
	Entry("URL DSN loses its password and query", "sqlserver://sa:s3cret@db.example:1433/main?password=s3cret&database=payroll", "sqlserver://sa@db.example:1433/main"),
	Entry("SQLite URL is kept", "sqlite:///var/data/side.db", "sqlite:///var/data/side.db"),
	Entry("SQLite file path is kept", "/var/data/side.db", "/var/data/side.db"),
	Entry("keyword DSN is withheld", "server=db.example;user id=sa;password=s3cret", "(redacted DSN)"),
)

var _ = Describe("Task run store", func() {
	var store *history.Store
	BeforeEach(func() { store = openStore() })

	run := func(status string, logs ...string) []clickytask.TaskSnapshot {
		group := clickytask.TaskSnapshot{ID: "Comparing", GroupID: "run-1", Type: "group", Name: "Comparing", Status: status, Kind: "sqlglot-compare",
			Labels: map[string]string{"target": "staging"}, StartedAt: "2026-10-26T09:30:00Z", Total: 1}
		child := clickytask.TaskSnapshot{ID: "t1", GroupID: "run-1", Type: "task", Name: "Inspect target", Status: status}
		for _, line := range logs {
			child.Logs = append(child.Logs, clickytask.LogEntry{Level: "info", Message: line})
		}
		return []clickytask.TaskSnapshot{group, child}
	}

	It("keeps the newest snapshot of a run and lists it by kind and label", func() {
		Expect(store.SaveRun(GinkgoT().Context(), "run-1", run("running"))).To(Succeed())
		Expect(store.SaveRun(GinkgoT().Context(), "run-1", run("success", "inspected main"))).To(Succeed())

		snapshots, err := store.Snapshot(GinkgoT().Context(), "run-1")
		Expect(err).NotTo(HaveOccurred())
		Expect(snapshots).To(Equal(run("success", "inspected main")))

		runs, err := store.Runs(GinkgoT().Context(), clickytask.RunFilter{Kind: "sqlglot-compare", Labels: map[string]string{"target": "staging"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(runs).To(HaveLen(1))
		Expect([]string{runs[0].ID, runs[0].Status}).To(Equal([]string{"run-1", "success"}))
		none, err := store.Runs(GinkgoT().Context(), clickytask.RunFilter{Kind: "sqlglot-patch"})
		Expect(err).NotTo(HaveOccurred())
		Expect(none).To(BeEmpty())
	})

	It("answers an unknown run with no snapshots and refuses to control a stored run", func() {
		snapshots, err := store.Snapshot(GinkgoT().Context(), "missing")
		Expect(err).NotTo(HaveOccurred())
		Expect(snapshots).To(BeEmpty())
		Expect(store.Control(GinkgoT().Context(), "run-1", clickytask.ControlRetry)).To(MatchError(ContainSubstring("cannot be controlled")))
	})

	It("refuses a run without a group snapshot", func() {
		Expect(store.SaveRun(GinkgoT().Context(), "run-1", run("success")[1:])).To(MatchError(ContainSubstring("no group snapshot")))
	})
})
