package sqlserver_test

import (
	"encoding/json"
	"slices"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func artifactRequest() migrate.ArtifactRequest {
	orders := migrate.Table{Name: migrate.Name{Schema: "dbo", Name: "orders"}, Columns: []migrate.Column{{Name: "id", Type: "int"}, {Name: "label", Type: "nvarchar(20)", Nullable: true}}}
	audit := migrate.Table{Name: migrate.Name{Schema: "audit", Name: "events"}, Columns: []migrate.Column{{Name: "id", Type: "int"}}}
	desired := orders
	desired.Columns = slices.Clone(orders.Columns)
	desired.Columns[1].Type = "nvarchar(100)"
	return migrate.ArtifactRequest{Schemas: []string{"dbo", "audit"}, Request: migrate.Request{Current: migrate.Schema{Tables: []migrate.Table{orders, audit}}, Desired: migrate.Schema{Tables: []migrate.Table{desired, audit}}}}
}

var _ = Describe("Immutable migration artifacts", func() {
	It("binds exact SQL and both snapshots to a deterministic hash without aliasing input", func() {
		input := artifactRequest()
		artifact, err := sqlserver.BuildArtifact(input)
		Expect(err).NotTo(HaveOccurred())
		Expect(artifact.Hash).To(MatchRegexp("^[0-9a-f]{64}$"))
		Expect(artifact.Schemas).To(Equal([]string{"audit", "dbo"}))
		Expect(artifact.Plan.Steps).To(HaveLen(1))
		Expect(artifact.Plan.Steps[0].SQL).To(Equal("ALTER TABLE [dbo].[orders] ALTER COLUMN [label] nvarchar(100) NULL"))
		slices.Reverse(input.Request.Current.Tables)
		slices.Reverse(input.Request.Desired.Tables)
		slices.Reverse(input.Schemas)
		again, err := sqlserver.BuildArtifact(input)
		Expect(err).NotTo(HaveOccurred())
		Expect(again).To(Equal(artifact))
		input.Request.Current.Tables[1].Columns[0].Type = "bigint"
		Expect(sqlserver.ValidateArtifact(artifact)).To(Succeed())
	})

	It("round trips JSON and rejects altered SQL, snapshots, scope and version", func() {
		artifact, err := sqlserver.BuildArtifact(artifactRequest())
		Expect(err).NotTo(HaveOccurred())
		body, err := json.Marshal(artifact)
		Expect(err).NotTo(HaveOccurred())
		for _, mutate := range []func(*migrate.Artifact){
			func(a *migrate.Artifact) { a.Plan.Steps[0].SQL = "DROP TABLE [dbo].[orders]" },
			func(a *migrate.Artifact) { a.Request.Desired.Tables[1].Columns[0].Type = "bigint" },
			func(a *migrate.Artifact) { a.Schemas[0] = "other" },
			func(a *migrate.Artifact) { a.Version++ },
		} {
			var decoded migrate.Artifact
			Expect(json.Unmarshal(body, &decoded)).To(Succeed())
			Expect(sqlserver.ValidateArtifact(decoded)).To(Succeed())
			mutate(&decoded)
			Expect(sqlserver.ValidateArtifact(decoded)).NotTo(Succeed())
		}
	})

	It("accepts only the baseline or complete desired state and rejects drift in preserved members", func() {
		input := artifactRequest()
		artifact, err := sqlserver.BuildArtifact(input)
		Expect(err).NotTo(HaveOccurred())
		state, err := sqlserver.CheckArtifact(artifact, input.Request.Current)
		Expect(err).NotTo(HaveOccurred())
		Expect(state).To(Equal(migrate.ArtifactPending))
		state, err = sqlserver.CheckArtifact(artifact, input.Request.Desired)
		Expect(err).NotTo(HaveOccurred())
		Expect(state).To(Equal(migrate.ArtifactApplied))
		input.Request.Current.Tables[0].Columns[0].Type = "bigint"
		_, err = sqlserver.CheckArtifact(artifact, input.Request.Current)
		Expect(err).To(MatchError(ContainSubstring("baseline drift")))
	})

	It("compares catalog expressions semantically when checking a no-op", func() {
		input := artifactRequest()
		input.Request.Current.Tables[0].Columns[0].Default = &migrate.Default{Name: "DF_orders_id", Expression: "(0)"}
		input.Request.Desired = input.Request.Current
		artifact, err := sqlserver.BuildArtifact(input)
		Expect(err).NotTo(HaveOccurred())
		input.Request.Current.Tables[0].Columns[0].Default.Expression = "((0))"
		state, err := sqlserver.CheckArtifact(artifact, input.Request.Current)
		Expect(err).NotTo(HaveOccurred())
		Expect(state).To(Equal(migrate.ArtifactApplied))
	})

	It("requires a nonempty explicit scope containing both snapshots", func() {
		input := artifactRequest()
		for _, scope := range [][]string{nil, {""}, {"dbo"}, {"dbo", "dbo", "audit"}} {
			input.Schemas = scope
			_, err := sqlserver.BuildArtifact(input)
			Expect(err).To(HaveOccurred())
		}
	})
})
