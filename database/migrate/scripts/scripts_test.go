package scripts_test

import (
	"testing"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestScripts(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Migration script fingerprints")
}

func Must[T any](value T, err error) T {
	GinkgoHelper()
	Expect(err).NotTo(HaveOccurred())
	return value
}

var _ = Describe("Consumed migration dependencies", func() {
	var bundle []scripts.Script
	var opts scripts.PlanOptions
	BeforeEach(func() {
		bundle = []scripts.Script{
			{ID: "a", Path: "a.sql", SQL: "SELECT 1", Phase: scripts.Pre},
			{ID: "b", Path: "b.sql", SQL: "SELECT 2", Phase: scripts.Post, DependsOn: []string{"a"}},
			{ID: "c", Path: "c.sql", SQL: "SELECT 3", Phase: scripts.Post, DependsOn: []string{"b"}},
			{ID: "d", Path: "d.sql", SQL: "SELECT 4", Phase: scripts.Post},
		}
		opts = scripts.PlanOptions{Target: "example", Scripts: bundle}
	})
	It("selects a fresh bundle and skips a fully recorded bundle", func() {
		plan := Must(scripts.Build(opts))
		Expect(selected(plan)).To(Equal([]string{"a", "b", "c", "d"}))
		opts.Recorded = recorded(plan)
		Expect(selected(Must(scripts.Build(opts)))).To(BeEmpty())
	})
	It("keeps failed dependents pending after their prerequisite commits", func() {
		initial := Must(scripts.Build(opts))
		opts.Recorded = recorded(initial)
		opts.Scripts[0].SQL = "SELECT 10"
		changed := Must(scripts.Build(opts))
		Expect(selected(changed)).To(Equal([]string{"a", "b", "c"}))
		opts.Recorded["a"] = changed.Scripts[0].State
		Expect(selected(Must(scripts.Build(opts)))).To(Equal([]string{"b", "c"}))
	})
	It("does not cascade an always execution without changed inputs", func() {
		opts.Scripts[0].Always = true
		opts.Recorded = recorded(Must(scripts.Build(opts)))
		Expect(selected(Must(scripts.Build(opts)))).To(Equal([]string{"a"}))
	})
	It("preserves a legacy content-only bundle until a prerequisite actually changes", func() {
		opts.Recorded = map[string]scripts.State{}
		for _, script := range bundle {
			hash := scripts.SourceHash(script.SQL)
			opts.Recorded[script.ID] = scripts.State{Version: 1, SourceHash: hash, Hash: hash}
		}
		plan := Must(scripts.Build(opts))
		Expect(selected(plan)).To(BeEmpty())
		for _, item := range plan.Scripts {
			Expect(item.Blocked).To(BeEmpty())
		}
		opts.Scripts[0].SQL = "SELECT 10"
		Expect(selected(Must(scripts.Build(opts)))).To(Equal([]string{"a", "b", "c"}))
	})
	It("requires explicit adoption when a legacy success gains a result dependency", func() {
		hash := scripts.SourceHash(bundle[1].SQL)
		opts.Recorded = map[string]scripts.State{"b": {Version: 1, SourceHash: hash, Hash: hash}}
		opts.Observations = map[string]map[string]scripts.Observation{"b": {"result:dbo.country": {Kind: "function-result", Hash: "current"}}}
		plan := Must(scripts.Build(opts))
		Expect(plan.Scripts[1].Blocked).To(ContainElement("adoption-required"))
		Expect(plan.Scripts[1].Selected).To(BeFalse())
	})
	It("invalidates only callers and dependents when a function result changes", func() {
		opts.Observations = map[string]map[string]scripts.Observation{"b": {"result:dbo.country": {Kind: "function-result", Hash: "ZW"}}}
		opts.Recorded = recorded(Must(scripts.Build(opts)))
		opts.Observations["b"]["result:dbo.country"] = scripts.Observation{Kind: "function-result", Hash: "MW"}
		plan := Must(scripts.Build(opts))
		Expect(selected(plan)).To(Equal([]string{"b", "c"}))
		Expect(plan.Scripts[1].Reasons).To(ContainElement("function-result-changed"))
	})
	It("refuses missing dependencies and phase inversions", func() {
		opts.Scripts[0].DependsOn = []string{"missing"}
		_, err := scripts.Build(opts)
		Expect(err).To(MatchError(ContainSubstring("missing")))
		opts.Scripts[0].DependsOn = []string{"b"}
		_, err = scripts.Build(opts)
		Expect(err).To(MatchError(ContainSubstring("post")))
	})
})

func selected(plan scripts.Plan) []string {
	ids := []string{}
	for _, item := range plan.Scripts {
		if item.Selected {
			ids = append(ids, item.Script.ID)
		}
	}
	return ids
}

func recorded(plan scripts.Plan) map[string]scripts.State {
	states := map[string]scripts.State{}
	for _, item := range plan.Scripts {
		states[item.Script.ID] = item.State
	}
	return states
}
