package scripts_test

import (
	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Effect propagation in one bundle plan", func() {
	asEnv, country := scripts.NewObjectName("dbo", "AsEnv"), scripts.NewObjectName("dbo", "country")
	report, unrelated := scripts.NewObjectName("dbo", "report"), scripts.NewObjectName("dbo", "unrelated")
	var opts scripts.PlanOptions
	index := func(id string) int {
		for i, s := range opts.Scripts {
			if s.ID == id {
				return i
			}
		}
		Fail("no script " + id)
		return -1
	}
	BeforeEach(func() {
		// w writes dbo.AsEnv; the catalog's dbo.country reads it; s calls
		// dbo.country and redefines dbo.report; s2 calls dbo.report.
		opts = scripts.PlanOptions{Target: "example", Scripts: []scripts.Script{
			{ID: "w", SQL: "UPDATE dbo.AsEnv SET Country=N'ZW'", Phase: scripts.Pre, Effects: &scripts.Effects{Writes: []scripts.ObjectName{asEnv}}},
			{ID: "s", SQL: "CREATE OR ALTER PROCEDURE dbo.report AS SELECT 1", Phase: scripts.Post,
				Effects: &scripts.Effects{Calls: []scripts.ObjectName{country}, Writes: []scripts.ObjectName{report}},
				Defines: map[scripts.ObjectName]scripts.Effects{report: {Reads: []scripts.ObjectName{unrelated}}}},
			{ID: "s2", SQL: "EXEC dbo.report", Phase: scripts.Post, Effects: &scripts.Effects{Calls: []scripts.ObjectName{report}}},
			{ID: "u", SQL: "SELECT * FROM dbo.unrelated", Phase: scripts.Post, Effects: &scripts.Effects{Reads: []scripts.ObjectName{unrelated}}},
		},
			Modules:      map[scripts.ObjectName]scripts.Effects{country: {Reads: []scripts.ObjectName{asEnv}}},
			Observations: map[string]map[string]scripts.Observation{"s": {"result:dbo.country": {Kind: "function-result", Hash: "ZW"}}},
		}
		opts.Recorded = recorded(Must(scripts.Build(opts)))
	})

	It("selects every script whose call graph reaches what a selected script writes, naming the writer", func() {
		opts.Scripts[index("w")].SQL = "UPDATE dbo.AsEnv SET Country=N'MW'"

		plan := Must(scripts.Build(opts))

		Expect(propagation(plan)).To(Equal(map[string]propagated{
			"w":  {Reasons: []string{"source-changed"}},
			"s":  {Reasons: []string{"upstream-write:dbo.asenv"}, Upstream: []scripts.UpstreamWrite{{Object: asEnv, Script: "w"}}, AfterApply: []scripts.AfterApplyInput{{Key: "result:dbo.country", Writers: []string{"w"}}}},
			"s2": {Reasons: []string{"upstream-write:dbo.report"}, Upstream: []scripts.UpstreamWrite{{Object: report, Script: "s"}}},
		}))
	})

	It("selects a post script reaching an object the schema step changes, naming the schema step as its writer", func() {
		opts.SchemaWrites = []scripts.ObjectName{unrelated}

		plan := Must(scripts.Build(opts))

		schema := propagated{Reasons: []string{"upstream-write:dbo.unrelated"}, Upstream: []scripts.UpstreamWrite{{Object: unrelated, Script: scripts.SchemaWriter}}}
		Expect(propagation(plan)).To(Equal(map[string]propagated{"u": schema, "s2": schema}), "s2 reaches it through the dbo.report s defines")
	})

	It("cascades nothing while the writer is unchanged", func() {
		Expect(selected(Must(scripts.Build(opts)))).To(BeEmpty())
	})

	It("plans pre and post scripts in one pass with pre scripts first", func() {
		opts.Scripts[index("w")].SQL = "UPDATE dbo.AsEnv SET Country=N'MW'"

		plan := Must(scripts.Build(opts))

		Expect(plan.Phase).To(BeEmpty())
		phases := []scripts.Phase{}
		for _, item := range plan.Scripts {
			phases = append(phases, item.Script.Phase)
		}
		Expect(phases).To(Equal([]scripts.Phase{scripts.Pre, scripts.Post, scripts.Post, scripts.Post}))
	})

	It("warns about a selected script with unknown effects without selecting it for that alone", func() {
		opts.Scripts[index("u")].Effects.Unknown = []string{"line 3: EXEC (@sql)"}
		opts.Recorded = recorded(Must(scripts.Build(opts)))
		Expect(selected(Must(scripts.Build(opts)))).To(BeEmpty())

		opts.Scripts[index("u")].SQL = "SELECT * FROM dbo.unrelated WHERE 1=1"
		plan := Must(scripts.Build(opts))

		Expect(selected(plan)).To(Equal([]string{"u"}))
		Expect(plan.Scripts[index("u")].EffectsUnknown).To(Equal([]string{"line 3: EXEC (@sql)"}))
		Expect(plan.Warnings).To(Equal([]string{"script u has unknown effects (line 3: EXEC (@sql)); apply re-inspects the plan after it runs"}))
	})

	It("reads a version 2 ledger row as the baseline of a script that gains effects", func() {
		withoutEffects := opts
		withoutEffects.Scripts = nil
		for _, s := range opts.Scripts {
			s.Effects, s.Defines = nil, nil
			withoutEffects.Scripts = append(withoutEffects.Scripts, s)
		}
		withoutEffects.Recorded = nil
		opts.Recorded = recorded(Must(scripts.Build(withoutEffects)))
		for id, state := range opts.Recorded {
			state.Version = 2
			opts.Recorded[id] = state
		}

		plan := Must(scripts.Build(opts))

		Expect(selected(plan)).To(BeEmpty())
		for _, item := range plan.Scripts {
			Expect(item.Blocked).To(BeEmpty())
		}
	})

	It("changes the plan hash, not the ledger state, when only the effects change", func() {
		before := Must(scripts.Build(opts))
		opts.Scripts[index("u")].Effects.Reads = append(opts.Scripts[index("u")].Effects.Reads, asEnv)

		after := Must(scripts.Build(opts))

		Expect(after.Hash).NotTo(Equal(before.Hash))
		Expect(after.Scripts[index("u")].State.Hash).To(Equal(before.Scripts[index("u")].State.Hash))
		Expect(after.Scripts[index("u")].State.EffectsHash).NotTo(Equal(before.Scripts[index("u")].State.EffectsHash))
	})
})

type propagated struct {
	Reasons    []string
	Upstream   []scripts.UpstreamWrite
	AfterApply []scripts.AfterApplyInput
}

func propagation(plan scripts.Plan) map[string]propagated {
	out := map[string]propagated{}
	for _, item := range plan.Scripts {
		if item.Selected {
			out[item.Script.ID] = propagated{Reasons: item.Reasons, Upstream: item.Upstream, AfterApply: item.AfterApply}
		}
	}
	return out
}
