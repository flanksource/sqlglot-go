package scripts_test

import (
	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Function values", func() {
	const dbName, country = "builtin:DB_NAME()", "result:dbo.country"
	var opts scripts.PlanOptions
	observe := func(database, value string) {
		builtin := scripts.Observation{Kind: scripts.KindBuiltin, Value: &database}
		opts.Observations = map[string]map[string]scripts.Observation{
			"a": {dbName: builtin},
			"d": {country: {Kind: scripts.KindResult, Hash: value, Value: &value}},
		}
	}
	BeforeEach(func() {
		opts = scripts.PlanOptions{Target: "example", Scripts: []scripts.Script{
			{ID: "a", SQL: "SELECT DB_NAME()", Phase: scripts.Pre},
			{ID: "b", SQL: "SELECT 2", Phase: scripts.Post, DependsOn: []string{"a"}},
			{ID: "c", SQL: "SELECT 3", Phase: scripts.Post, DependsOn: []string{"b"}},
			{ID: "d", SQL: "SELECT dbo.country()", Phase: scripts.Post},
		}}
		observe("QA", "ZW")
	})
	It("selects the scripts reading a built-in, and their dependents, when its value changes", func() {
		opts.Recorded = recorded(Must(scripts.Build(opts)))
		Expect(selected(Must(scripts.Build(opts)))).To(BeEmpty())
		observe("UAT", "ZW")
		changed := Must(scripts.Build(opts))
		Expect(selected(changed)).To(Equal([]string{"a", "b", "c"}))
		Expect(changed.Scripts[0].Reasons).To(Equal([]string{"builtin-changed"}))

		failed := changed.Scripts[0].State
		failed.Outcome = "failed"
		opts.Recorded["a"] = failed
		Expect(selected(Must(scripts.Build(opts)))).To(Equal([]string{"a", "b", "c"}), "the dependents skipped after it stay selected")
		opts.Recorded = recorded(changed)
		Expect(selected(Must(scripts.Build(opts)))).To(BeEmpty())
	})
	It("never selects a script for a built-in its last run did not record", func() {
		opts.Recorded = recorded(Must(scripts.Build(opts)))
		for id, state := range opts.Recorded {
			state.Values = nil
			opts.Recorded[id] = state
		}
		observe("UAT", "ZW")
		Expect(selected(Must(scripts.Build(opts)))).To(BeEmpty())
	})
	It("selects neither a script nor its dependents for a recorded input that is no longer tracked", func() {
		opts.Observations["a"]["definition:dbo.country"] = scripts.Observation{Kind: scripts.KindDefinition, Hash: "v1"}
		opts.Recorded = recorded(Must(scripts.Build(opts)))
		delete(opts.Observations["a"], "definition:dbo.country")
		Expect(selected(Must(scripts.Build(opts)))).To(BeEmpty())
	})
	It("blocks a script whose environment call cannot be observed", func() {
		opts.Observations["a"][dbName] = scripts.Observation{Kind: scripts.KindBuiltin, Error: "permission denied"}
		item := Must(scripts.Build(opts)).Scripts[0]
		Expect([]any{item.Selected, item.Blocked, item.State.Values}).To(Equal([]any{false, []string{dbName + ": permission denied"}, map[string]*string(nil)}))
	})
	It("lists each function's value with what every script using it last recorded", func() {
		opts.Recorded = recorded(Must(scripts.Build(opts)))
		observe("UAT", "ZW")
		qa, uat, zw := "QA", "UAT", "ZW"
		Expect(scripts.FunctionInputs(Must(scripts.Build(opts)).Scripts, opts.Recorded)).To(Equal([]scripts.FunctionInput{
			{Name: "DB_NAME()", Builtin: true, Sampled: true, Value: &uat, Uses: []scripts.FunctionUse{{Script: "a", Recorded: true, Previous: &qa, ValueChanged: true}}},
			{Name: "dbo.country", Sampled: true, Value: &zw, Uses: []scripts.FunctionUse{{Script: "d", Recorded: true, Previous: &zw}}},
		}))
	})
	It("marks a changed result whose previous value was never recorded, and a changed definition", func() {
		opts.Observations["d"]["definition:dbo.country"] = scripts.Observation{Kind: scripts.KindDefinition, Hash: "v1"}
		opts.Recorded = recorded(Must(scripts.Build(opts)))
		state := opts.Recorded["d"]
		state.Values = nil
		opts.Recorded["d"] = state
		observe("QA", "MW")
		opts.Observations["d"]["definition:dbo.country"] = scripts.Observation{Kind: scripts.KindDefinition, Hash: "v2"}
		Expect(scripts.FunctionInputs(Must(scripts.Build(opts)).Scripts, opts.Recorded)[1].Uses).To(Equal([]scripts.FunctionUse{
			{Script: "d", ValueChanged: true, DefinitionChanged: true}}))
	})
})
