package sqlserver_test

import (
	"slices"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	"github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// pureDialect is the pure planner registered under a name of its own: the
// database half owns "sqlserver".
type pureDialect struct{ sqlserver.Planner }

const pureDialectName = "sqlserver-pure-test"

func (pureDialect) Name() string { return pureDialectName }

func init() { engine.Register(pureDialect{}) }

func completeRequest(current, desired migrate.Schema) engine.PlanRequest {
	return engine.PlanRequest{
		Dialect:      pureDialectName,
		Scope:        engine.Scope{Mode: engine.ScopeComplete, Schemas: []string{"dbo"}},
		Current:      current,
		Desired:      desired,
		Capabilities: engine.Capabilities{Fingerprint: "pure-test", TransactionalDDL: true},
	}
}

func planArtifact(request engine.PlanRequest) engine.Artifact {
	artifact, err := engine.Plan(request)
	Expect(err).NotTo(HaveOccurred())
	return artifact
}

// planBetween plans current to desired in complete scope, optionally limited to some changes.
func planBetween(current, desired migrate.Schema, selection ...string) (engine.Artifact, error) {
	request := completeRequest(current, desired)
	request.Selection = selection
	return engine.Plan(request)
}

func mustPlanBetween(current, desired migrate.Schema, selection ...string) engine.Artifact {
	GinkgoHelper()
	artifact, err := planBetween(current, desired, selection...)
	Expect(err).NotTo(HaveOccurred())
	return artifact
}

// statementsOf flattens units and ops in plan order.
func statementsOf(a engine.Artifact) []string {
	out := []string{}
	for _, u := range a.Units {
		for _, op := range u.Ops {
			for _, s := range op.Statements {
				out = append(out, s.SQL)
			}
		}
	}
	return out
}

func artifactChangeIDs(a engine.Artifact) []string {
	out := make([]string, 0, len(a.Changes))
	for _, c := range a.Changes {
		out = append(out, c.ID)
	}
	return out
}

func sortedChangeIDs(a engine.Artifact) []string {
	ids := artifactChangeIDs(a)
	slices.Sort(ids)
	return ids
}

func changeID(action, kind, name, member string) string {
	return `["` + action + `","` + kind + `","dbo","` + name + `","` + member + `"]`
}

var (
	charOrders    = migrate.Name{Schema: "dbo", Name: "orders"}
	charCustomers = migrate.Name{Schema: "dbo", Name: "customers"}
	charLoader    = migrate.Name{Schema: "dbo", Name: "load_orders"}
)

func charColumn(name, typ string, nullable bool) migrate.Column {
	return migrate.Column{Name: name, Type: typ, Nullable: nullable}
}

func charTable(name migrate.Name, columns ...migrate.Column) migrate.Table {
	return migrate.Table{Name: name, Columns: columns}
}

func schemaOf(tables ...migrate.Table) migrate.Schema { return migrate.Schema{Tables: tables} }

func charID(action, kind string, name migrate.Name, member string) string {
	return changeID(action, kind, name.Name, member)
}

func charProcedure(body string) migrate.Schema {
	return migrate.Schema{Definitions: []migrate.Definition{{Name: charLoader, Kind: "PROCEDURE", SQL: "CREATE PROCEDURE dbo.load_orders AS " + body}}}
}

// keyedOrders is a customers/orders pair carrying every constraint kind.
func keyedOrders() migrate.Schema {
	customers := charTable(charCustomers, charColumn("id", "int", false))
	customers.Indexes = []migrate.Index{{Name: "PK_customers", Primary: true, Constraint: true, Clustered: true, Columns: []migrate.IndexColumn{{Name: "id"}}}}
	status := charColumn("status", "nvarchar(20)", false)
	status.Default = &migrate.Default{Name: "DF_orders_status", Expression: "(N'new')"}
	orders := charTable(charOrders, charColumn("id", "int", false), charColumn("customer_id", "int", false), status)
	orders.Indexes = []migrate.Index{
		{Name: "PK_orders", Primary: true, Constraint: true, Clustered: true, Columns: []migrate.IndexColumn{{Name: "id"}}},
		{Name: "IX_orders_customer", Columns: []migrate.IndexColumn{{Name: "customer_id"}}},
	}
	orders.ForeignKeys = []migrate.ForeignKey{{Name: "FK_orders_customers", Columns: []string{"customer_id"}, References: charCustomers, ReferencedColumns: []string{"id"}}}
	orders.Checks = []migrate.Check{{Name: "CK_orders_id", Expression: "([id]>(0))"}}
	return schemaOf(customers, orders)
}

func quantityOrders(typ string) migrate.Schema {
	qty := charColumn("qty", typ, false)
	qty.Default = &migrate.Default{Name: "DF_orders_qty", Expression: "((0))"}
	return schemaOf(charTable(charOrders, charColumn("id", "int", false), qty))
}

func labelledOrders(label migrate.Column, index migrate.Index) migrate.Schema {
	table := charTable(charOrders, charColumn("id", "int", false), label)
	table.Indexes = []migrate.Index{index}
	return schemaOf(table)
}
