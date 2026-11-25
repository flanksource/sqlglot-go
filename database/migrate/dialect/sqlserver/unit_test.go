package sqlserver

import (
	"context"

	"github.com/flanksource/sqlglot-go/database/capabilities"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("capabilities", func() {
	It("keys the planner's TRY_CONVERT feature by the capability the probe reports", func() {
		Expect(string(capabilities.BackendCapabilityTryConvert)).To(Equal(planner.FeatureTryConvert))
	})

	It("maps each probed capability to a feature and takes transactional DDL from its own capability", func() {
		probed := capabilities.BackendCapabilities{Backend: "sqlserver", Version: "16.0.1000", Database: "orders", Features: map[capabilities.BackendCapability]capabilities.BackendCapabilityStatus{
			capabilities.BackendCapabilityTransactionalDDL: {Supported: true},
			capabilities.BackendCapabilityTryConvert:       {Supported: false},
		}}
		mapped, err := mapCapabilities(probed)
		Expect(err).NotTo(HaveOccurred())
		fingerprint, err := probed.Fingerprint()
		Expect(err).NotTo(HaveOccurred())
		Expect(mapped).To(Equal(engine.Capabilities{Fingerprint: fingerprint, TransactionalDDL: true, Features: map[string]bool{"transactionalDDL": true, "tryConvert": false}}))
	})

	It("fails when the probe did not report transactional DDL", func() {
		_, err := mapCapabilities(capabilities.BackendCapabilities{Backend: "sqlserver", Features: map[capabilities.BackendCapability]capabilities.BackendCapabilityStatus{
			capabilities.BackendCapabilityTryConvert: {Supported: true},
		}})
		Expect(err).To(MatchError(ContainSubstring("transactionalDDL")))
	})
})

var _ = Describe("refusal reasons", func() {
	table := migrate.Name{Schema: "dbo", Name: "orders"}

	DescribeTable("an index that cannot be re-created names itself and why",
		func(h indexHeader, parts []indexPart, want string) {
			dep := (&reader{requested: []engine.ColumnRef{{Table: table, Column: "id"}}}).indexDependent(h, parts)
			Expect(dep.Refusal).To(Equal(want))
		},
		Entry("a plain rowstore index has none", indexHeader{table: table, name: "IX_a", typ: 2, rowLocks: true, pageLocks: true}, nil, ""),
		Entry("disabled", indexHeader{table: table, name: "IX_a", typ: 2, disabled: true, rowLocks: true, pageLocks: true}, nil,
			"index IX_a on [dbo].[orders] is disabled"),
		Entry("columnstore", indexHeader{table: table, name: "CCI", typ: 5, typeDesc: "CLUSTERED COLUMNSTORE", rowLocks: true, pageLocks: true, compLeast: "COLUMNSTORE", compMost: "COLUMNSTORE"}, nil,
			"index CCI on [dbo].[orders] uses COLUMNSTORE data compression and is a CLUSTERED COLUMNSTORE index"),
		Entry("partitioned", indexHeader{table: table, name: "IX_p", typ: 2, spaceType: "PS", spaceName: "ps_date", rowLocks: true, pageLocks: true}, nil,
			"index IX_p on [dbo].[orders] is partitioned by ps_date"),
		Entry("mixed per-partition compression", indexHeader{table: table, name: "IX_m", typ: 2, compLeast: "NONE", compMost: "PAGE", partitions: 2, rowLocks: true, pageLocks: true}, nil,
			"index IX_m on [dbo].[orders] has mixed per-partition data compression (NONE to PAGE)"),
		Entry("the key of a full-text index", indexHeader{table: table, name: "UQ_ft", typ: 2, fullTextKey: true, rowLocks: true, pageLocks: true}, nil,
			"index UQ_ft on [dbo].[orders] is the key index of a full-text index"),
	)

	It("describes schema-bound objects, calling a view with an index an indexed view", func() {
		on := engine.ColumnRef{Table: table, Column: "id"}
		view := schemaBoundDependent(on, migrate.Name{Schema: "dbo", Name: "v_orders"}, "V", true)
		Expect(view.Kind).To(Equal(engine.DependentView))
		Expect(view.Refusal).To(Equal("indexed view [dbo].[v_orders] is bound to [dbo].[orders].id, and the planner cannot re-create it"))
		fn := schemaBoundDependent(on, migrate.Name{Schema: "dbo", Name: "f"}, "IF", false)
		Expect([]any{fn.Kind, fn.Refusal}).To(Equal([]any{dependentFunction, "schema-bound function [dbo].[f] is bound to [dbo].[orders].id, and the planner cannot re-create it"}))
	})
})

var _ = Describe("definition normalisation", func() {
	DescribeTable("fill factor 0 and 100 are the server default",
		func(in, want int) { Expect(normalizeFillFactor(in)).To(Equal(want)) },
		Entry("unset", 0, 0), Entry("100", 100, 0), Entry("90", 90, 90),
	)

	DescribeTable("compression is read from the partitions",
		func(least, most string, partitions int, want, refusal string) {
			got, reason := compressionOf(least, most, partitions)
			Expect([]string{got, reason}).To(Equal([]string{want, refusal}))
		},
		Entry("none", "NONE", "NONE", 1, "", ""),
		Entry("page", "PAGE", "PAGE", 1, "PAGE", ""),
		Entry("row", "ROW", "ROW", 1, "ROW", ""),
		Entry("mixed", "NONE", "ROW", 2, "", "has mixed per-partition data compression (NONE to ROW)"),
		Entry("several equal partitions", "PAGE", "PAGE", 3, "", "has 3 partitions"),
		Entry("columnstore", "COLUMNSTORE", "COLUMNSTORE", 1, "", "uses COLUMNSTORE data compression"),
	)

	DescribeTable("only a filegroup other than the default is recorded",
		func(spaceType, name string, isDefault bool, want, refusal string) {
			got, reason := filegroupOf(spaceType, name, isDefault)
			Expect([]string{got, reason}).To(Equal([]string{want, refusal}))
		},
		Entry("the default filegroup", "FG", "PRIMARY", true, "", ""),
		Entry("another filegroup", "FG", "INDEXES", false, "INDEXES", ""),
		Entry("a partition scheme", "PS", "ps_date", false, "", "is partitioned by ps_date"),
	)

	It("spells referential actions as the inspector does and sorts included columns", func() {
		Expect(normalizeAction("SET_NULL")).To(Equal("SET NULL"))
		Expect(sortedColumns([]string{"b", "a"})).To(Equal([]string{"a", "b"}))
	})

	It("numbers each requested column and binds its names, leaving ids out of parameters", func() {
		text, args := requestedColumns([]engine.ColumnRef{
			{Table: migrate.Name{Schema: "dbo", Name: "a"}, Column: "x"},
			{Table: migrate.Name{Schema: "s", Name: "b"}, Column: "y"},
		})
		Expect(text).To(ContainSubstring("VALUES (0, @p1, @p2, @p3), (1, @p4, @p5, @p6)"))
		Expect(args).To(Equal([]any{"dbo", "a", "x", "s", "b", "y"}))
		Expect(idPairs([][2]int{{1, 2}, {3, 4}})).To(Equal("(1, 2), (3, 4)"))
	})

	It("renders a statistic as a complete statement with its filter and options", func() {
		s := statisticRow{table: migrate.Name{Schema: "dbo", Name: "t"}, name: "ST_a", hasFilter: true, filter: " ([a]>(0)) ", noRecompute: true}
		Expect(statisticDefinition(s, []string{"a", "b"})).To(Equal("CREATE STATISTICS [ST_a] ON [dbo].[t] ([a], [b]) WHERE ([a]>(0)) WITH NORECOMPUTE"))
	})
})

var _ = Describe("Reconcile", func() {
	name := migrate.Name{Schema: "dbo", Name: "orders"}
	live := migrate.Schema{Tables: []migrate.Table{{Name: name, Columns: []migrate.Column{
		{Name: "label", Type: "varchar(20)", Collation: "Latin1_General_CI_AS", Default: &migrate.Default{Name: "DF__orders__label__1A", SystemNamed: true, Expression: "('x')"}},
		{Name: "id", Type: "varchar(36)", Collation: "Latin1_General_CI_AS"},
	}}}}
	reconcile := func(desired migrate.Table) migrate.Table {
		out, err := Dialect{}.Reconcile(context.Background(), nil, live, migrate.Schema{Tables: []migrate.Table{desired}})
		Expect(err).NotTo(HaveOccurred())
		return out.Tables[0]
	}

	It("inherits the live collation of a text column that leaves it unset", func() {
		got := reconcile(migrate.Table{Name: name, Columns: []migrate.Column{{Name: "label", Type: "varchar(20)"}}})
		Expect(got.Columns[0].Collation).To(Equal("Latin1_General_CI_AS"))
	})

	It("spells a collation that differs only in case like the live one, and keeps a real difference", func() {
		same := reconcile(migrate.Table{Name: name, Columns: []migrate.Column{{Name: "label", Type: "varchar(20)", Collation: "latin1_general_ci_as"}}})
		Expect(same.Columns[0].Collation).To(Equal("Latin1_General_CI_AS"))
		other := reconcile(migrate.Table{Name: name, Columns: []migrate.Column{{Name: "label", Type: "varchar(20)", Collation: "Latin1_General_CS_AS"}}})
		Expect(other.Columns[0].Collation).To(Equal("Latin1_General_CS_AS"))
	})

	It("never gives a collation to a column that is not text", func() {
		got := reconcile(migrate.Table{Name: name, Columns: []migrate.Column{{Name: "id", Type: "uniqueidentifier"}}})
		Expect(got.Columns[0].Collation).To(BeEmpty())
	})

	It("does not change the schema it was given", func() {
		desired := migrate.Schema{Tables: []migrate.Table{{Name: name, Columns: []migrate.Column{{Name: "label", Type: "varchar(20)"}}}}}
		_, err := Dialect{}.Reconcile(context.Background(), nil, live, desired)
		Expect(err).NotTo(HaveOccurred())
		Expect(desired.Tables[0].Columns[0].Collation).To(BeEmpty())
	})
})
