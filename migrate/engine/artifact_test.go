package engine_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func rehash(a engine.Artifact) engine.Artifact {
	a.Hash = ""
	body, err := json.Marshal(a)
	Expect(err).NotTo(HaveOccurred())
	sum := sha256.Sum256(body)
	a.Hash = hex.EncodeToString(sum[:])
	return a
}

var _ = Describe("Artifact", func() {
	var f *fakeDialect
	var req engine.PlanRequest
	table := func(name string) migrate.Table {
		return migrate.Table{Name: tableName(name), Columns: []migrate.Column{{Name: "id", Type: "int"}}}
	}

	BeforeEach(func() {
		f = newFake()
		f.changes = []migrate.Change{change("add", "column", "orders", "note"), change("drop", "column", "orders", "old")}
		f.offline = []engine.Dependent{dependent(engine.DependentIndex, "orders", "ix_note", col("orders", "note"))}
		f.forcing = []engine.Forcing{{Change: f.changes[0].ID, Columns: []engine.ColumnRef{col("orders", "note")}}}
		req = f.request(engine.ScopeComplete)
		req.Current = migrate.Schema{Tables: []migrate.Table{table("orders"), table("items")}}
		req.Desired = migrate.Schema{Tables: []migrate.Table{table("items"), table("orders")}}
	})

	It("hashes identically however the set-valued inputs are ordered", func() {
		req.Selection = []string{f.changes[0].ID, f.changes[1].ID}
		req.Dependents = []engine.Dependent{
			dependent(engine.DependentIndex, "orders", "ix_b", col("orders", "note")),
			dependent(engine.DependentCheck, "orders", "ck_a", col("orders", "note")),
		}
		first, err := engine.Plan(req)
		Expect(err).NotTo(HaveOccurred())

		shuffled := req
		shuffled.Current = migrate.Schema{Tables: []migrate.Table{table("items"), table("orders")}}
		shuffled.Selection = []string{f.changes[1].ID, f.changes[0].ID}
		shuffled.Dependents = []engine.Dependent{req.Dependents[1], req.Dependents[0]}
		second, err := engine.Plan(shuffled)
		Expect(err).NotTo(HaveOccurred())
		Expect(second.Hash).To(Equal(first.Hash))
		Expect(first.Baseline.Tables[0].Name.Name).To(Equal("items"))
		Expect(first.Changes).To(HaveLen(5))
	})

	It("hashes differently when the selection differs", func() {
		all, err := engine.Plan(req)
		Expect(err).NotTo(HaveOccurred())
		req.Selection = []string{f.changes[0].ID}
		some, err := engine.Plan(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(some.Hash).NotTo(Equal(all.Hash))
	})

	It("validates a fresh artifact and one that has been through JSON", func() {
		a, err := engine.Plan(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(engine.Validate(a)).To(Succeed())
		body, err := json.Marshal(a)
		Expect(err).NotTo(HaveOccurred())
		var decoded engine.Artifact
		Expect(json.Unmarshal(body, &decoded)).To(Succeed())
		Expect(engine.Validate(decoded)).To(Succeed())
	})

	It("detects a tampered statement through the hash", func() {
		a, err := engine.Plan(req)
		Expect(err).NotTo(HaveOccurred())
		a.Units[0].Ops[0].Statements[0].SQL = "DROP DATABASE prod"
		Expect(engine.Validate(a)).To(MatchError(ContainSubstring("hash mismatch")))
	})

	It("detects a tampered statement even when the hash is recomputed, by re-planning", func() {
		a, err := engine.Plan(req)
		Expect(err).NotTo(HaveOccurred())
		a.Units[0].Ops[0].Statements[0].SQL = "DROP DATABASE prod"
		Expect(engine.Validate(rehash(a))).To(MatchError(ContainSubstring("does not match its snapshots")))
	})

	It("rejects a version it does not know", func() {
		a, err := engine.Plan(req)
		Expect(err).NotTo(HaveOccurred())
		a.Version = 1
		Expect(engine.Validate(a)).To(MatchError(ContainSubstring("unsupported migration artifact version 1")))
	})

	It("lists the selected destructive changes, never rebuilds, as the acknowledgements", func() {
		a, err := engine.Plan(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(engine.Acknowledgements(a)).To(Equal([]string{f.changes[1].ID}))
	})

	It("rejects an unregistered dialect", func() {
		req.Dialect = "nosuch"
		_, err := engine.Plan(req)
		Expect(err).To(MatchError(ContainSubstring(`unknown dialect "nosuch"`)))
	})
})

var _ = Describe("Dialect registry", func() {
	It("panics on a duplicate or empty name", func() {
		f := newFake()
		Expect(func() { engine.Register(f) }).To(PanicWith(ContainSubstring("registered twice")))
		f.name = ""
		Expect(func() { engine.Register(f) }).To(PanicWith(ContainSubstring("empty name")))
	})
})
