package sqlserver_test

import (
	"slices"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func changeID(action, kind, name, member string) string {
	return `["` + action + `","` + kind + `","dbo","` + name + `","` + member + `"]`
}

// selectionRequest adds a customers table referenced by a new orders foreign
// key, adds a note column to orders and drops orders.legacy.
func selectionRequest() migrate.ArtifactRequest {
	orders := migrate.Table{Name: migrate.Name{Schema: "dbo", Name: "orders"}, Columns: []migrate.Column{{Name: "id", Type: "int"}, {Name: "customer_id", Type: "int"}, {Name: "legacy", Type: "int", Nullable: true}}}
	customers := migrate.Table{Name: migrate.Name{Schema: "dbo", Name: "customers"}, Columns: []migrate.Column{{Name: "id", Type: "int"}}, Indexes: []migrate.Index{{Name: "PK_customers", Primary: true, Constraint: true, Clustered: true, Columns: []migrate.IndexColumn{{Name: "id"}}}}}
	desired := orders
	desired.Columns = []migrate.Column{orders.Columns[0], orders.Columns[1], {Name: "note", Type: "nvarchar(50)", Nullable: true}}
	desired.ForeignKeys = []migrate.ForeignKey{{Name: "FK_orders_customers", Columns: []string{"customer_id"}, References: customers.Name, ReferencedColumns: []string{"id"}}}
	return migrate.ArtifactRequest{Schemas: []string{"dbo"}, Request: migrate.Request{
		Current: migrate.Schema{Tables: []migrate.Table{orders}},
		Desired: migrate.Schema{Tables: []migrate.Table{customers, desired}},
	}}
}

var (
	addCustomers = changeID("add", "table", "customers", "")
	addNote      = changeID("add", "column", "orders", "note")
	dropLegacy   = changeID("drop", "column", "orders", "legacy")
	addOrdersFK  = changeID("add", "foreign-key", "orders", "FK_orders_customers")
)

func stepChanges(plan migrate.Plan) []string {
	var ids []string
	for _, step := range plan.Steps {
		if !slices.Contains(ids, step.Change.ID) {
			ids = append(ids, step.Change.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

var _ = Describe("Selective SQL Server plans", func() {
	It("lists every candidate change with its structural requirements", func() {
		plan, err := sqlserver.Plan(selectionRequest().Request)
		Expect(err).NotTo(HaveOccurred())
		requires := map[string][]string{}
		for _, c := range plan.Changes {
			requires[c.ID] = c.Requires
		}
		Expect(requires).To(Equal(map[string][]string{
			addCustomers: nil,
			addNote:      nil,
			dropLegacy:   nil,
			addOrdersFK:  {addCustomers},
		}))
	})
	It("plans only the selected changes while still listing every candidate", func() {
		request := selectionRequest().Request
		request.Selection = []string{addNote}
		plan, err := sqlserver.Plan(request)
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Changes).To(HaveLen(4))
		Expect(stepChanges(plan)).To(Equal([]string{addNote}))
		Expect(plan.Steps[0].SQL).To(Equal("ALTER TABLE [dbo].[orders] ADD [note] nvarchar(50) NULL"))
	})
	It("plans a foreign key together with the table it references", func() {
		request := selectionRequest().Request
		request.Selection = []string{addOrdersFK, addCustomers}
		plan, err := sqlserver.Plan(request)
		Expect(err).NotTo(HaveOccurred())
		Expect(stepChanges(plan)).To(ConsistOf(addCustomers, addOrdersFK))
		Expect(plan.Steps[0].Change.ID).To(Equal(addCustomers))
	})
	It("refuses a selection missing a required change", func() {
		request := selectionRequest().Request
		request.Selection = []string{addOrdersFK}
		_, err := sqlserver.Plan(request)
		Expect(err).To(MatchError(ContainSubstring("requires unselected changes " + addCustomers)))
	})
	It("refuses a selected change that is not in the diff", func() {
		request := selectionRequest().Request
		request.Selection = []string{changeID("add", "table", "missing", "")}
		_, err := sqlserver.Plan(request)
		Expect(err).To(MatchError(ContainSubstring("is not in the diff")))
	})
	It("binds the selection into a reproducible artifact that is applied once its changes land", func() {
		input := selectionRequest()
		input.Request.Selection = []string{dropLegacy, addNote}
		artifact, err := sqlserver.BuildArtifact(input)
		Expect(err).NotTo(HaveOccurred())
		Expect(artifact.Request.Selection).To(Equal([]string{addNote, dropLegacy}))
		Expect(sqlserver.ValidateArtifact(artifact)).To(Succeed())

		tampered := artifact
		tampered.Request.Selection = []string{addNote}
		Expect(sqlserver.ValidateArtifact(tampered)).To(MatchError(ContainSubstring("hash mismatch")))

		state, err := sqlserver.CheckArtifact(artifact, input.Request.Current)
		Expect(err).NotTo(HaveOccurred())
		Expect(state).To(Equal(migrate.ArtifactPending))

		partial := input.Request.Current
		partial.Tables = []migrate.Table{input.Request.Current.Tables[0]}
		partial.Tables[0].Columns = []migrate.Column{partial.Tables[0].Columns[0], partial.Tables[0].Columns[1], {Name: "note", Type: "nvarchar(50)", Nullable: true}}
		state, err = sqlserver.CheckArtifact(artifact, partial)
		Expect(err).NotTo(HaveOccurred())
		Expect(state).To(Equal(migrate.ArtifactApplied))
	})
	It("requires acknowledging only the selected destructive changes", func() {
		input := selectionRequest()
		input.Request.Selection = []string{addNote}
		artifact, err := sqlserver.BuildArtifact(input)
		Expect(err).NotTo(HaveOccurred())
		Expect(sqlserver.Acknowledgements(artifact)).To(BeEmpty())
		input.Request.Selection = []string{addNote, dropLegacy}
		artifact, err = sqlserver.BuildArtifact(input)
		Expect(err).NotTo(HaveOccurred())
		Expect(sqlserver.Acknowledgements(artifact)).To(Equal([]string{dropLegacy}))
	})
	It("keeps the hash of an artifact without a selection unchanged by the selection field", func() {
		artifact, err := sqlserver.BuildArtifact(artifactRequest())
		Expect(err).NotTo(HaveOccurred())
		Expect(artifact.Request.Selection).To(BeNil())
		Expect(sqlserver.ValidateArtifact(artifact)).To(Succeed())
	})
})
