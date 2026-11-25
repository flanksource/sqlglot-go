package engine_test

import (
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Offline plans", func() {
	var f *fakeDialect
	ordersAdd := change("add", "column", "orders", "note")

	BeforeEach(func() {
		f = newFake()
		f.changes = []migrate.Change{ordersAdd}
		f.offlineCaps = engine.Capabilities{TransactionalDDL: true, Features: map[string]bool{"alwaysThere": true}}
	})

	It("asks the dialect for its offline capabilities when the request carries none, and records them", func() {
		req := f.request(engine.ScopeComplete)
		req.Capabilities = engine.Capabilities{}
		a, err := engine.Plan(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(a.Capabilities).To(Equal(f.offlineCaps))
		Expect(a.Capabilities.Fingerprint).To(BeEmpty())
		Expect(a.Units).To(HaveLen(1))
		Expect(a.Units[0].Transactional).To(BeTrue())
	})

	It("leaves caller-supplied capabilities alone", func() {
		req := f.request(engine.ScopeComplete)
		req.Capabilities = engine.Capabilities{Fingerprint: "live-1", TransactionalDDL: false}
		a, err := engine.Plan(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(a.Capabilities).To(Equal(req.Capabilities))
		Expect(a.Units[0].Transactional).To(BeFalse())
	})

	It("re-plans an offline artifact to the same hash", func() {
		req := f.request(engine.ScopeComplete)
		req.Capabilities = engine.Capabilities{}
		a, err := engine.Plan(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(engine.Validate(a)).To(Succeed())
	})
})
