package sqlserver_test

import (
	"context"
	"database/sql"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	"github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL Server dialect dependents and dangling targets", func() {
	type found struct {
		Kind  engine.DependentKind
		Table string
		Name  string
		On    []engine.ColumnRef
	}
	summarize := func(deps []engine.Dependent) []found {
		out := []found{}
		for _, d := range deps {
			out = append(out, found{d.Kind, d.Table.Name, d.Name, d.On})
		}
		return out
	}

	It("derives every dependent of a column from the current schema alone, over-including expressions and leaving the default to the column's change", func() {
		deps, err := sqlserver.Planner{}.Dependents(context.Background(), nil, guidSchema("varchar(36)", "varchar(36)"), []engine.ColumnRef{accountIDRef})
		Expect(err).NotTo(HaveOccurred())
		on := []engine.ColumnRef{accountIDRef}
		Expect(summarize(deps)).To(Equal([]found{
			{engine.DependentCheck, "accounts", "CK_accounts_id", on},
			{engine.DependentForeignKey, "orders", "FK_orders_accounts", on},
			{engine.DependentIndex, "accounts", "IX_accounts_id", on},
			{engine.DependentIndex, "accounts", "IX_accounts_note", on},
			{engine.DependentPrimaryKey, "accounts", systemPKName, on},
		}))
		Expect(deps[1].ForeignKey.References).To(Equal(accounts))
		Expect(deps[2].Index.Included).To(Equal([]string{"note"}))
	})

	It("unions the forcing columns a foreign key depends on", func() {
		deps, err := sqlserver.Planner{}.Dependents(context.Background(), nil, guidSchema("varchar(36)", "varchar(36)"), []engine.ColumnRef{orderAccountID, accountIDRef})
		Expect(err).NotTo(HaveOccurred())
		for _, d := range deps {
			if d.Kind == engine.DependentForeignKey {
				Expect(d.On).To(Equal([]engine.ColumnRef{accountIDRef, orderAccountID}))
			}
		}
	})

	It("uses the kind the change set gives a unique constraint, which is index", func() {
		t := charTable(charOrders, charColumn("id", "int", false), charColumn("label", "varchar(20)", false))
		t.Indexes = []migrate.Index{{Name: "UQ_label", Unique: true, Constraint: true, Columns: []migrate.IndexColumn{{Name: "label"}}}}
		deps, err := sqlserver.Planner{}.Dependents(context.Background(), nil, schemaOf(t), []engine.ColumnRef{{Table: charOrders, Column: "label"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(summarize(deps)).To(HaveLen(1))
		Expect(deps[0].Kind).To(Equal(engine.DependentIndex))
		Expect(deps[0].Index.Constraint).To(BeTrue())
	})

	It("refuses live dependents and unknown columns, saying what is wrong", func() {
		_, err := sqlserver.Planner{}.Dependents(context.Background(), (*sql.DB)(nil), migrate.Schema{}, nil)
		Expect(err).To(MatchError(ContainSubstring("requires the database dialect")))
		_, err = sqlserver.Planner{}.Dependents(context.Background(), nil, guidSchema("varchar(36)", "varchar(36)"), []engine.ColumnRef{{Table: accounts, Column: "ghost"}})
		Expect(err).To(MatchError(ContainSubstring("[dbo].[accounts].ghost: no such column")))
	})

	It("answers the database-only methods with an error that says so", func() {
		p := sqlserver.Planner{}
		_, err := p.Capabilities(context.Background(), &sql.DB{})
		Expect(err).To(MatchError(ContainSubstring("requires the database dialect")))
		_, err = p.Inspect(context.Background(), nil, engine.Scope{})
		Expect(err).To(MatchError(ContainSubstring("requires the database dialect")))
		_, err = p.Open(context.Background(), nil, engine.SessionOptions{})
		Expect(err).To(MatchError(ContainSubstring("requires the database dialect")))
		_, err = p.Reconcile(context.Background(), nil, migrate.Schema{}, migrate.Schema{})
		Expect(err).To(MatchError(ContainSubstring("requires the database dialect")))
		Expect(p.Exec(context.Background(), nil, engine.Statement{})).To(MatchError(ContainSubstring("requires the database dialect")))
		Expect(p.Name()).To(Equal("sqlserver"))
	})

	Describe("a foreign key whose target is outside the schema", func() {
		withForeignKey := func(extra ...migrate.Column) migrate.Schema {
			t := charTable(charOrders, append([]migrate.Column{charColumn("id", "int", false), charColumn("customer_id", "int", false)}, extra...)...)
			t.ForeignKeys = []migrate.ForeignKey{{Name: "FK_orders_customers", Columns: []string{"customer_id"}, References: charCustomers, ReferencedColumns: []string{"id"}}}
			return schemaOf(t)
		}
		idsOf := func(current, desired migrate.Schema) []string {
			diff, err := sqlserver.Planner{}.Diff(current, desired)
			Expect(err).NotTo(HaveOccurred())
			out := []string{}
			for _, c := range diff.Changes() {
				out = append(out, c.ID)
			}
			return out
		}

		It("reports no change for the missing table, only for what differs", func() {
			Expect(idsOf(withForeignKey(), withForeignKey())).To(BeEmpty())
			Expect(idsOf(withForeignKey(), withForeignKey(charColumn("note", "int", true)))).To(Equal([]string{changeID("add", "column", "orders", "note")}))
			Expect(idsOf(schemaOf(charTable(charOrders, charColumn("id", "int", false), charColumn("customer_id", "int", false))), withForeignKey())).
				To(Equal([]string{changeID("add", "foreign-key", "orders", "FK_orders_customers")}))
		})

		It("plans the change without lowering anything for the missing table", func() {
			artifact := planArtifact(completeRequest(withForeignKey(), withForeignKey(charColumn("note", "int", true))))
			Expect(statementsOf(artifact)).To(Equal([]string{"ALTER TABLE [dbo].[orders] ADD [note] int NULL"}))
		})
	})

	Describe("a routine whose dependency is outside the schema", func() {
		routine := migrate.Name{Schema: "dbo", Name: "order_count"}
		withRoutine := func(body string) migrate.Schema {
			return migrate.Schema{Tables: []migrate.Table{}, Definitions: []migrate.Definition{{
				Name: routine, Kind: "PROCEDURE", Dependencies: []migrate.Name{charOrders},
				SQL: "CREATE PROCEDURE [dbo].[order_count] AS " + body,
			}}}
		}

		It("diffs a schema narrowed to the routine, reporting only the routine's own change", func() {
			same, err := sqlserver.Planner{}.Diff(withRoutine("SELECT COUNT(*) FROM dbo.orders"), withRoutine("SELECT COUNT(*) FROM dbo.orders"))
			Expect(err).NotTo(HaveOccurred())
			Expect(same.Changes()).To(BeEmpty())

			edited, err := sqlserver.Planner{}.Diff(withRoutine("SELECT COUNT(*) FROM dbo.orders"), withRoutine("SELECT 1 FROM dbo.orders"))
			Expect(err).NotTo(HaveOccurred())
			Expect(edited.Changes()).To(HaveLen(1))
			Expect(edited.Changes()[0].Object).To(Equal(routine))
		})
	})
})
