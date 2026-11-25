package sqlserver_test

import (
	"github.com/flanksource/sqlglot-go/migrate"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// selectionSchemas add a customers table referenced by a new orders foreign
// key, add a note column to orders and drop orders.legacy.
func selectionSchemas() (current, desired migrate.Schema) {
	orders := charTable(charOrders, charColumn("id", "int", false), charColumn("customer_id", "int", false), charColumn("legacy", "int", true))
	customers := charTable(charCustomers, charColumn("id", "int", false))
	customers.Indexes = []migrate.Index{{Name: "PK_customers", Primary: true, Constraint: true, Clustered: true, Columns: []migrate.IndexColumn{{Name: "id"}}}}
	wanted := charTable(charOrders, orders.Columns[0], orders.Columns[1], charColumn("note", "nvarchar(50)", true))
	wanted.ForeignKeys = []migrate.ForeignKey{{Name: "FK_orders_customers", Columns: []string{"customer_id"}, References: charCustomers, ReferencedColumns: []string{"id"}}}
	return schemaOf(orders), schemaOf(customers, wanted)
}

var _ = Describe("Selective SQL Server plans", func() {
	var (
		addCustomers = changeID("add", "table", "customers", "")
		addNote      = changeID("add", "column", "orders", "note")
		dropLegacy   = changeID("drop", "column", "orders", "legacy")
		addOrdersFK  = changeID("add", "foreign-key", "orders", "FK_orders_customers")
		current      migrate.Schema
		desired      migrate.Schema
	)
	BeforeEach(func() { current, desired = selectionSchemas() })

	It("lists every candidate change with its structural requirements", func() {
		requires := map[string][]string{}
		for _, c := range mustPlanBetween(current, desired).Changes {
			requires[c.ID] = c.Requires
		}
		Expect(requires).To(Equal(map[string][]string{
			addCustomers: nil,
			addNote:      nil,
			dropLegacy:   nil,
			addOrdersFK:  {addCustomers},
		}))
	})

	It("plans only the selected changes", func() {
		artifact := mustPlanBetween(current, desired, addNote)
		Expect(artifact.Selection).To(Equal([]string{addNote}))
		Expect(statementsOf(artifact)).To(Equal([]string{"ALTER TABLE [dbo].[orders] ADD [note] nvarchar(50) NULL"}))
	})

	It("plans a foreign key together with the table it references", func() {
		artifact := mustPlanBetween(current, desired, addOrdersFK, addCustomers)
		statements := statementsOf(artifact)
		Expect(statements).To(HaveLen(2))
		Expect(statements[0]).To(HavePrefix("CREATE TABLE [dbo].[customers]"))
		Expect(statements[1]).To(ContainSubstring("FOREIGN KEY"))
	})

	It("refuses a selection missing a required change", func() {
		_, err := planBetween(current, desired, addOrdersFK)
		Expect(err).To(MatchError(ContainSubstring("requires unselected changes " + addCustomers)))
	})

	It("refuses a selected change that is not in the diff", func() {
		_, err := planBetween(current, desired, changeID("add", "table", "missing", ""))
		Expect(err).To(MatchError(ContainSubstring("selected changes are not in the diff")))
	})
})
