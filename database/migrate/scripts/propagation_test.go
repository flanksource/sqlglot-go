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

	It("selects only the writer, marking what its readers observe as known after it", func() {
		opts.Scripts[index("w")].SQL = "UPDATE dbo.AsEnv SET Country=N'MW'"

		plan := Must(scripts.Build(opts))

		Expect(selected(plan)).To(Equal([]string{"w"}))
		Expect(plan.Scripts[index("s")].AfterApply).To(Equal([]scripts.AfterApplyInput{{Key: "result:dbo.country", Writers: []string{"w"}}}))
	})

	It("selects no post script for what the schema step changes", func() {
		opts.SchemaWrites = []scripts.ObjectName{unrelated, asEnv}

		plan := Must(scripts.Build(opts))

		Expect(selected(plan)).To(BeEmpty())
		Expect(plan.Scripts[index("s")].AfterApply).To(Equal([]scripts.AfterApplyInput{{Key: "result:dbo.country", Writers: []string{scripts.SchemaWriter}}}))
	})

	Describe("a script's reads", func() {
		read := scripts.Read{SQL: "SELECT * FROM dbo.unrelated WHERE id = 1", Objects: []scripts.ObjectName{unrelated}}
		answer := func(digest string) scripts.ReadAnswer { return scripts.ReadAnswer{Digest: digest, Rows: 1} }
		record := func(reads map[string]scripts.ReadAnswer) {
			u := opts.Recorded["u"]
			u.Reads = reads
			opts.Recorded["u"] = u
		}
		BeforeEach(func() {
			opts.Reads = map[string][]scripts.Read{"u": {read}}
			opts.Scripts = append(opts.Scripts, scripts.Script{ID: "v", SQL: "SELECT 1", Phase: scripts.Post, DependsOn: []string{"u"}})
			opts.Recorded = recorded(Must(scripts.Build(opts)))
			record(map[string]scripts.ReadAnswer{read.Key(): answer("before")})
		})
		DescribeTable("select it when their answer changed since it ran",
			func(now scripts.ReadAnswer, want []string, changed bool) {
				opts.Answers = map[string]scripts.ReadAnswer{read.SQL: now}

				plan := Must(scripts.Build(opts))

				before := answer("before")
				Expect(selected(plan)).To(Equal(want), "never a script depending on it")
				u := plan.Scripts[index("u")]
				Expect(u.Reads).To(Equal([]scripts.PlannedRead{{Read: read, Recorded: &before, Answer: &now, Changed: changed}}))
				Expect(u.State.Hash).To(Equal(opts.Recorded["u"].Hash))
				if changed {
					Expect(u.Reasons).To(Equal([]string{scripts.ReadChanged}))
				}
			},
			Entry("the same answer", answer("before"), []string{}, false),
			Entry("another answer", answer("after"), []string{"u"}, true),
			Entry("a failed query", scripts.ReadAnswer{Error: "Invalid object name"}, []string{}, false),
		)
		DescribeTable("are not compared",
			func(change func()) {
				change()
				for _, item := range Must(scripts.Build(opts)).Scripts {
					if item.Script.ID == "u" {
						Expect(item.Reads).To(Equal([]scripts.PlannedRead{{Read: read}}))
						Expect(item.Reasons).NotTo(ContainElement(scripts.ReadChanged))
					}
				}
			},
			Entry("with no recorded answer", func() { record(nil) }),
			Entry("once its source changed", func() { opts.Scripts[index("u")].SQL += " WHERE 1=1" }),
		)
		DescribeTable("show both answers",
			func(before, after scripts.ReadAnswer, want string) {
				Expect(scripts.PlannedRead{Recorded: &before, Answer: &after}.Change()).To(Equal(want))
			},
			Entry("a value", scripts.ReadAnswer{Rows: 1, Value: new("ZW")}, scripts.ReadAnswer{Rows: 0}, `"ZW" → 0 rows`),
			Entry("as many rows", scripts.ReadAnswer{Rows: 1}, scripts.ReadAnswer{Rows: 1}, "1 row → 1 row with other values"),
		)
		It("list what it reads outside a query, or through a routine, as untracked", func() {
			opts.UntrackedReads = map[string][]scripts.ObjectName{"s": {unrelated}}
			untracked := map[string][]scripts.ObjectName{}
			for _, item := range Must(scripts.Build(opts)).Scripts {
				untracked[item.Script.ID] = item.UntrackedReads
			}
			Expect(untracked).To(Equal(map[string][]scripts.ObjectName{"w": nil, "s": {unrelated}, "s2": {unrelated}, "u": nil, "v": nil}),
				"s calls dbo.country, tracked by its result")
		})
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
