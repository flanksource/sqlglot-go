package workflow

import (
	"encoding/json"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("a plan result", func() {
	accounts := migrate.Name{Schema: "dbo", Name: "accounts"}
	table := func(refType string) migrate.Table {
		return migrate.Table{
			Name:    accounts,
			Columns: []migrate.Column{{Name: "id", Type: "int"}, {Name: "ref", Type: refType}},
			Indexes: []migrate.Index{{Name: "IX_accounts_ref", Columns: []migrate.IndexColumn{{Name: "ref"}}}},
		}
	}
	retype := func() PlanResult {
		artifact, err := engine.Plan(engine.PlanRequest{
			Dialect: "sqlserver", Scope: engine.Scope{Mode: engine.ScopeComplete, Schemas: []string{"dbo"}},
			Current: migrate.Schema{Tables: []migrate.Table{table("varchar(36)")}}, Desired: migrate.Schema{Tables: []migrate.Table{table("uniqueidentifier")}},
			Capabilities: engine.Capabilities{Fingerprint: "offline", TransactionalDDL: true},
		})
		Expect(err).NotTo(HaveOccurred())
		data, err := json.Marshal(artifact)
		Expect(err).NotTo(HaveOccurred())
		plan, err := Review(data, "sqlserver", "dbo")
		Expect(err).NotTo(HaveOccurred())
		return plan
	}

	It("groups a forced rebuild under the change that forced it", func() {
		plan := retype()
		modify := `["modify","column","dbo","accounts","ref"]`
		Expect(plan.Changes).To(HaveLen(1))
		Expect(plan.Changes[0].ID).To(Equal(modify))
		Expect(plan.Changes[0].Rebuilds).To(HaveLen(1))
		rebuild := plan.Changes[0].Rebuilds[0]
		Expect([]string{rebuild.ID, rebuild.Action, rebuild.Kind, rebuild.Member, rebuild.Cause}).To(Equal([]string{
			`["rebuild","index","dbo","accounts","IX_accounts_ref"]`, "rebuild", "index", "IX_accounts_ref", modify,
		}))
	})

	It("lists the preconditions of each unit and the statements in execution order with their unit and phase", func() {
		plan := retype()
		Expect(plan.Units).To(HaveLen(1))
		unit := plan.Units[0]
		Expect(unit.Transactional).To(BeTrue())
		Expect(unit.Preconditions).To(HaveLen(1))
		Expect(unit.Preconditions[0].ID).To(Equal(`["modify","column","dbo","accounts","ref"]#guid`))
		Expect(unit.Preconditions[0].Message).To(ContainSubstring("not canonical GUIDs"))

		phases := []string{}
		for _, s := range plan.Steps {
			Expect(s.Unit).To(Equal(unit.ID))
			phases = append(phases, s.Phase)
		}
		Expect(phases).To(Equal([]string{"drop", "alter", "create"}))
		Expect(unit.Steps).To(HaveLen(len(plan.Steps)))
		Expect(plan.Scope.Mode).To(Equal(engine.ScopeComplete))
		Expect(plan.Schema).To(Equal("dbo"))
	})

	It("asks for the acknowledgement of the destructive change, never of its rebuild, and renders SQL Server batches", func() {
		plan := retype()
		Expect(plan.Acks).To(Equal([]string{`["modify","column","dbo","accounts","ref"]`}))
		Expect(plan.SQL).To(ContainSubstring("\nGO\n"))
		Expect(plan.Applicable).To(BeTrue())
	})

	It("is refused for another dialect or another schema than the target's", func() {
		data := []byte(retype().Artifact)
		_, err := Review(data, "postgres", "dbo")
		Expect(err).To(MatchError(ContainSubstring("the artifact is for sqlserver, not the target's postgres")))
		_, err = Review(data, "sqlserver", "app")
		Expect(err).To(MatchError(ContainSubstring(`not the target's "app"`)))
	})
})
