//go:build integration

package sqlserver

import (
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("an artifact planned offline", Label("integration"), func() {
	It("applies a unit in one transaction, so a failing second statement leaves the first one undone", func(ctx SpecContext) {
		mustExec(ctx, db, "CREATE SCHEMA offlineunit",
			"CREATE TABLE offlineunit.t (id int NOT NULL, qty int NOT NULL CONSTRAINT DF_t_qty DEFAULT (0))",
			"INSERT INTO offlineunit.t (id, qty) VALUES (1, 1000)")
		name := migrate.Name{Schema: "offlineunit", Name: "t"}
		current := inspect(ctx, db, declared("offlineunit", "t")).Schema
		desired := cloneSchema(current)
		column(tableOf(desired, name), "qty").Type = "tinyint"

		artifact, err := engine.Plan(engine.PlanRequest{
			Dialect: "sqlserver", Scope: declared("offlineunit", "t"), Current: current, Desired: desired,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(artifact.Capabilities.Fingerprint).To(BeEmpty())
		Expect(artifact.Units).To(HaveLen(1))
		Expect(artifact.Units[0].Transactional).To(BeTrue())
		Expect(artifact.Units[0].Ops).To(HaveLen(1))
		Expect(artifact.Units[0].Ops[0].Statements).To(HaveLen(3), "drop the default, alter the column, bind the default again")

		_, err = applyArtifact(ctx, db, artifact)
		Expect(err).To(HaveOccurred())

		Expect(scalar[int](ctx, db, "SELECT COUNT(*) FROM sys.default_constraints WHERE name = N'DF_t_qty'")).To(Equal(1), "the dropped default came back with the rollback")
		Expect(scalar[string](ctx, db, "SELECT TYPE_NAME(system_type_id) FROM sys.columns WHERE object_id = OBJECT_ID(N'offlineunit.t') AND name = N'qty'")).To(Equal("int"))
	})
})
