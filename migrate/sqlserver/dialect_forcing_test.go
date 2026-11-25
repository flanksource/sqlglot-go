package sqlserver_test

import (
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	"github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL Server dialect forcing", func() {
	label := func(column migrate.Column, extra func(*migrate.Table)) migrate.Schema {
		t := charTable(charOrders, charColumn("id", "int", false), column)
		if extra != nil {
			extra(&t)
		}
		return schemaOf(t)
	}
	indexed := func(t *migrate.Table) {
		t.Indexes = []migrate.Index{{Name: "IX_label", Columns: []migrate.IndexColumn{{Name: "label"}}}}
	}
	primary := func(t *migrate.Table) {
		t.Indexes = []migrate.Index{{Name: "PK_orders", Primary: true, Unique: true, Constraint: true, Columns: []migrate.IndexColumn{{Name: "label"}}}}
	}
	unique := func(t *migrate.Table) {
		t.Indexes = []migrate.Index{{Name: "UQ_label", Unique: true, Constraint: true, Columns: []migrate.IndexColumn{{Name: "label"}}}}
	}
	checked := func(t *migrate.Table) {
		t.Checks = []migrate.Check{{Name: "CK_label", Expression: "([label]<>'')"}}
	}
	foreignKeyed := func(t *migrate.Table) {
		t.ForeignKeys = []migrate.ForeignKey{{Name: "FK_label", Columns: []string{"label"}, References: charCustomers, ReferencedColumns: []string{"code"}}}
	}
	both := func(fns ...func(*migrate.Table)) func(*migrate.Table) {
		return func(t *migrate.Table) {
			for _, fn := range fns {
				fn(t)
			}
		}
	}
	collated := func(c migrate.Column, collation string) migrate.Column { c.Collation = collation; return c }
	defaulted := func(c migrate.Column) migrate.Column {
		c.Default = &migrate.Default{Name: "DF_label", Expression: "('')"}
		return c
	}
	customers := charTable(charCustomers, charColumn("code", "varchar(20)", false))
	labelID := changeID("modify", "column", "orders", "label")

	DescribeTable("forces dependents only where ALTER COLUMN refuses them",
		func(from, to migrate.Column, extra func(*migrate.Table), expected []engine.Forcing) {
			current, desired := label(from, extra), label(to, extra)
			current.Tables = append(current.Tables, customers)
			desired.Tables = append(desired.Tables, customers)
			diff, err := sqlserver.Planner{}.Diff(current, desired)
			Expect(err).NotTo(HaveOccurred())
			if expected == nil {
				Expect(diff.Forcing()).To(BeEmpty())
				return
			}
			Expect(diff.Forcing()).To(Equal(expected))
		},
		Entry("widening a varchar under a non-primary index is allowed",
			charColumn("label", "varchar(20)", false), charColumn("label", "varchar(100)", false), indexed, nil),
		Entry("widening a varchar to (max) under an index is allowed",
			charColumn("label", "varchar(20)", false), charColumn("label", "varchar(max)", false), indexed, nil),
		Entry("widening a varchar under a primary key forces it, with a retype",
			charColumn("label", "varchar(20)", false), charColumn("label", "varchar(100)", false), primary,
			[]engine.Forcing{{Change: labelID, Columns: []engine.ColumnRef{{Table: charOrders, Column: "label"}}, Retype: true}}),
		Entry("widening a varchar under a unique constraint forces it, since a foreign key may reference it",
			charColumn("label", "varchar(20)", false), charColumn("label", "varchar(100)", false), unique,
			[]engine.Forcing{{Change: labelID, Columns: []engine.ColumnRef{{Table: charOrders, Column: "label"}}, Retype: true}}),
		Entry("widening a varchar under a check is a length-only change and is allowed",
			charColumn("label", "varchar(20)", false), charColumn("label", "varchar(100)", false), checked, nil),
		Entry("widening a varchar on the child side of a foreign key forces it",
			charColumn("label", "varchar(20)", false), charColumn("label", "varchar(100)", false), foreignKeyed,
			[]engine.Forcing{{Change: labelID, Columns: []engine.ColumnRef{{Table: charOrders, Column: "label"}}, Retype: true}}),
		Entry("widening a varchar whose default the change re-binds forces nothing",
			defaulted(charColumn("label", "varchar(20)", false)), defaulted(charColumn("label", "varchar(100)", false)), nil, nil),
		Entry("narrowing a varchar under an index forces it",
			charColumn("label", "varchar(100)", false), charColumn("label", "varchar(20)", false), indexed,
			[]engine.Forcing{{Change: labelID, Columns: []engine.ColumnRef{{Table: charOrders, Column: "label"}}, Retype: true}}),
		Entry("loosening nullability under a check forces it, without a retype",
			charColumn("label", "varchar(20)", false), charColumn("label", "varchar(20)", true), checked,
			[]engine.Forcing{{Change: labelID, Columns: []engine.ColumnRef{{Table: charOrders, Column: "label"}}}}),
		Entry("changing nullability of an int forces it, since a user statistic may use it",
			charColumn("label", "int", false), charColumn("label", "int", true), nil,
			[]engine.Forcing{{Change: labelID, Columns: []engine.ColumnRef{{Table: charOrders, Column: "label"}}}}),
		Entry("changing collation under an index forces it, without a retype",
			collated(charColumn("label", "varchar(20)", false), "Latin1_General_CI_AS"), collated(charColumn("label", "varchar(20)", false), "Latin1_General_BIN2"), indexed,
			[]engine.Forcing{{Change: labelID, Columns: []engine.ColumnRef{{Table: charOrders, Column: "label"}}}}),
		Entry("changing the type forces it, with a retype",
			charColumn("label", "varchar(36)", false), charColumn("label", "uniqueidentifier", false), nil,
			[]engine.Forcing{{Change: labelID, Columns: []engine.ColumnRef{{Table: charOrders, Column: "label"}}, Retype: true}}),
		Entry("changing between varchar and nvarchar forces it",
			charColumn("label", "varchar(20)", false), charColumn("label", "nvarchar(20)", false), indexed,
			[]engine.Forcing{{Change: labelID, Columns: []engine.ColumnRef{{Table: charOrders, Column: "label"}}, Retype: true}}),
		Entry("widening a fixed-length char forces it",
			charColumn("label", "char(10)", false), charColumn("label", "char(20)", false), indexed,
			[]engine.Forcing{{Change: labelID, Columns: []engine.ColumnRef{{Table: charOrders, Column: "label"}}, Retype: true}}),
		Entry("an unchanged column forces nothing, whatever depends on it",
			charColumn("label", "varchar(20)", false), charColumn("label", "varchar(20)", false), both(primary, checked), nil),
	)

	It("lists nothing for a change that is not a column modification", func() {
		current := label(charColumn("label", "varchar(20)", false), primary)
		desired := schemaOf(charTable(charOrders, charColumn("id", "int", false), charColumn("label", "varchar(20)", false), charColumn("note", "int", true)))
		desired.Tables[0].Indexes = current.Tables[0].Indexes
		diff, err := sqlserver.Planner{}.Diff(current, desired)
		Expect(err).NotTo(HaveOccurred())
		Expect(diff.Forcing()).To(BeEmpty())
	})
})
