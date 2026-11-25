package sqlserver_test

import (
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	"github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL Server dialect rebuild rendering", func() {
	table := migrate.Name{Schema: "dbo", Name: "odd]orders"}
	on := []engine.ColumnRef{{Table: table, Column: "label"}}

	lowerRebuild := func(dep engine.Dependent) ([]engine.Op, error) {
		diff, err := sqlserver.Planner{}.Diff(migrate.Schema{}, migrate.Schema{})
		Expect(err).NotTo(HaveOccurred())
		id := changeID("rebuild", string(dep.Kind), "odd]orders", dep.Name)
		return diff.Lower(nil, []engine.Rebuild{{
			Change:    migrate.Change{ID: id, Action: "rebuild", Kind: string(dep.Kind), Object: dep.Table, Member: dep.Name},
			Dependent: dep,
		}})
	}
	sqlOf := func(op engine.Op) []string {
		out := []string{}
		for _, s := range op.Statements {
			out = append(out, s.SQL)
		}
		return out
	}

	DescribeTable("drops and re-creates a dependent from its live definition, under the same name",
		func(dep engine.Dependent, drop, create []string, sizeOfData bool) {
			ops, err := lowerRebuild(dep)
			Expect(err).NotTo(HaveOccurred())
			Expect(ops).To(HaveLen(2))
			Expect(ops[0].Phase).To(Equal(engine.PhaseDrop))
			Expect(ops[1].Phase).To(Equal(engine.PhaseCreate))
			Expect(ops[1].Requires).To(Equal([]string{ops[0].ID}))
			Expect(ops[0].Object).To(Equal(table))
			Expect(sqlOf(ops[0])).To(Equal(drop))
			Expect(sqlOf(ops[1])).To(Equal(create))
			Expect(ops[1].Statements[0].SizeOfData).To(Equal(sizeOfData))
		},
		Entry("an index with every option",
			engine.Dependent{Kind: engine.DependentIndex, Table: table, Name: "IX_all", On: on, Index: &migrate.Index{
				Name: "IX_all", Unique: true, Clustered: true, Columns: []migrate.IndexColumn{{Name: "label", Descending: true}, {Name: "b]c"}},
				Included: []string{"note"}, Filter: "([label] IS NOT NULL)", FillFactor: 70, PadIndex: true, IgnoreDupKey: true, NoRecompute: true,
				NoRowLocks: true, NoPageLocks: true, Compression: "ROW", Filegroup: "FG1",
			}},
			[]string{"DROP INDEX [IX_all] ON [dbo].[odd]]orders]"},
			[]string{"CREATE UNIQUE CLUSTERED INDEX [IX_all] ON [dbo].[odd]]orders] ([label] DESC, [b]]c]) INCLUDE ([note]) WHERE ([label] IS NOT NULL) " +
				"WITH (PAD_INDEX = ON, FILLFACTOR = 70, IGNORE_DUP_KEY = ON, STATISTICS_NORECOMPUTE = ON, ALLOW_ROW_LOCKS = OFF, ALLOW_PAGE_LOCKS = OFF, DATA_COMPRESSION = ROW) ON [FG1]"},
			true),
		Entry("a plain index leaves the server defaults unsaid",
			engine.Dependent{Kind: engine.DependentIndex, Table: table, Name: "IX_plain", On: on, Index: &migrate.Index{Name: "IX_plain", Columns: []migrate.IndexColumn{{Name: "label"}}, FillFactor: 100}},
			[]string{"DROP INDEX [IX_plain] ON [dbo].[odd]]orders]"},
			[]string{"CREATE NONCLUSTERED INDEX [IX_plain] ON [dbo].[odd]]orders] ([label])"}, true),
		Entry("a unique constraint",
			engine.Dependent{Kind: engine.DependentIndex, Table: table, Name: "UQ_label", On: on, Index: &migrate.Index{Name: "UQ_label", Unique: true, Constraint: true, Columns: []migrate.IndexColumn{{Name: "label"}}, IgnoreDupKey: true}},
			[]string{"ALTER TABLE [dbo].[odd]]orders] DROP CONSTRAINT [UQ_label]"},
			[]string{"ALTER TABLE [dbo].[odd]]orders] ADD CONSTRAINT [UQ_label] UNIQUE NONCLUSTERED ([label]) WITH (IGNORE_DUP_KEY = ON)"}, true),
		Entry("a primary key keeps its system-generated name",
			engine.Dependent{Kind: engine.DependentPrimaryKey, Table: table, Name: "PK__odd__3214EC07", On: on, Index: &migrate.Index{Name: "PK__odd__3214EC07", Primary: true, Unique: true, Constraint: true, Clustered: true, Columns: []migrate.IndexColumn{{Name: "label"}}}},
			[]string{"ALTER TABLE [dbo].[odd]]orders] DROP CONSTRAINT [PK__odd__3214EC07]"},
			[]string{"ALTER TABLE [dbo].[odd]]orders] ADD CONSTRAINT [PK__odd__3214EC07] PRIMARY KEY CLUSTERED ([label])"}, true),
		Entry("a foreign key comes back trusted, with its actions",
			engine.Dependent{Kind: engine.DependentForeignKey, Table: table, Name: "FK_x", On: on, ForeignKey: &migrate.ForeignKey{
				Name: "FK_x", Columns: []string{"label"}, References: charCustomers, ReferencedColumns: []string{"code"}, OnDelete: "set null", OnUpdate: "CASCADE", NotForReplication: true,
			}},
			[]string{"ALTER TABLE [dbo].[odd]]orders] DROP CONSTRAINT [FK_x]"},
			[]string{
				"ALTER TABLE [dbo].[odd]]orders] WITH CHECK ADD CONSTRAINT [FK_x] FOREIGN KEY ([label]) REFERENCES [dbo].[customers] ([code]) ON DELETE SET NULL ON UPDATE CASCADE NOT FOR REPLICATION",
				"ALTER TABLE [dbo].[odd]]orders] CHECK CONSTRAINT [FK_x]",
			}, true),
		Entry("a check comes back trusted",
			engine.Dependent{Kind: engine.DependentCheck, Table: table, Name: "CK_x", On: on, Check: &migrate.Check{Name: "CK_x", Expression: "([label]<>'')", NotForReplication: true}},
			[]string{"ALTER TABLE [dbo].[odd]]orders] DROP CONSTRAINT [CK_x]"},
			[]string{
				"ALTER TABLE [dbo].[odd]]orders] WITH CHECK ADD CONSTRAINT [CK_x] CHECK NOT FOR REPLICATION (([label]<>''))",
				"ALTER TABLE [dbo].[odd]]orders] CHECK CONSTRAINT [CK_x]",
			}, true),
		Entry("a default binds back to its column",
			engine.Dependent{Kind: engine.DependentDefault, Table: table, Name: "DF_x", On: on, Definition: "((0))"},
			[]string{"ALTER TABLE [dbo].[odd]]orders] DROP CONSTRAINT [DF_x]"},
			[]string{"ALTER TABLE [dbo].[odd]]orders] ADD CONSTRAINT [DF_x] DEFAULT ((0)) FOR [label]"}, false),
		Entry("a statistic is re-created from its definition",
			engine.Dependent{Kind: engine.DependentStatistic, Table: table, Name: "ST_x", On: on, Definition: "CREATE STATISTICS [ST_x] ON [dbo].[odd]]orders] ([label])"},
			[]string{"DROP STATISTICS [dbo].[odd]]orders].[ST_x]"},
			[]string{"CREATE STATISTICS [ST_x] ON [dbo].[odd]]orders] ([label])"}, false),
	)

	DescribeTable("refuses a dependent it cannot render, naming it",
		func(dep engine.Dependent, message string) {
			_, err := lowerRebuild(dep)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("a unique constraint under the engine's own kind",
			engine.Dependent{Kind: engine.DependentUniqueConstraint, Table: table, Name: "UQ_x", Index: &migrate.Index{Name: "UQ_x", Columns: []migrate.IndexColumn{{Name: "label"}}}}, "kind index with Index.Constraint"),
		Entry("a view",
			engine.Dependent{Kind: engine.DependentView, Table: table, Name: "v", Definition: "CREATE VIEW v AS SELECT 1"}, `unsupported dependent kind "view"`),
		Entry("an unknown compression",
			engine.Dependent{Kind: engine.DependentIndex, Table: table, Name: "IX_x", Index: &migrate.Index{Name: "IX_x", Columns: []migrate.IndexColumn{{Name: "label"}}, Compression: "COLUMNSTORE"}}, `compression "COLUMNSTORE" is not ROW or PAGE`),
		Entry("a primary key with a filter",
			engine.Dependent{Kind: engine.DependentPrimaryKey, Table: table, Name: "PK_x", Index: &migrate.Index{Name: "PK_x", Primary: true, Columns: []migrate.IndexColumn{{Name: "label"}}, Filter: "(1=1)"}}, "cannot have included columns or a filter"),
		Entry("a statistic without its definition",
			engine.Dependent{Kind: engine.DependentStatistic, Table: table, Name: "ST_x"}, "no CREATE STATISTICS text"),
		Entry("a foreign key with an unknown action",
			engine.Dependent{Kind: engine.DependentForeignKey, Table: table, Name: "FK_x", ForeignKey: &migrate.ForeignKey{Name: "FK_x", Columns: []string{"a"}, References: charCustomers, ReferencedColumns: []string{"b"}, OnDelete: "EXPLODE"}}, `invalid reference action "EXPLODE"`),
		Entry("a default that names no single column",
			engine.Dependent{Kind: engine.DependentDefault, Table: table, Name: "DF_x", Definition: "((0))"}, "the one column it binds to in On"),
	)

	It("refuses to rebuild a default that its own column's change already re-binds", func() {
		request := completeRequest(guidSchema("varchar(36)", "varchar(36)"), guidSchema("uniqueidentifier", "uniqueidentifier"))
		request.Dependents = []engine.Dependent{{Kind: engine.DependentDefault, Table: accounts, Name: "DF_accounts_id", On: []engine.ColumnRef{accountIDRef}, Definition: "(newid())"}}
		_, err := engine.Plan(request)
		Expect(err).To(MatchError(ContainSubstring("DF_accounts_id of [dbo].[accounts].id is re-bound by change")))
	})

	It("lowers an empty selection to no ops and a nil selection to every change", func() {
		desired := schemaOf(charTable(charOrders, charColumn("id", "int", false)))
		diff, err := sqlserver.Planner{}.Diff(migrate.Schema{}, desired)
		Expect(err).NotTo(HaveOccurred())
		none, err := diff.Lower([]string{}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(none).To(BeEmpty())
		all, err := diff.Lower(nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(all).To(HaveLen(1))
		Expect(all[0].ID).To(Equal("create:" + changeID("add", "table", "orders", "")))
	})

	It("marks the statements that read every row", func() {
		current := schemaOf(charTable(charOrders, charColumn("id", "int", false), charColumn("label", "varchar(20)", true)))
		desired := schemaOf(charTable(charOrders, charColumn("id", "bigint", false), charColumn("label", "varchar(20)", true), charColumn("note", "int", true)))
		desired.Tables[0].Indexes = []migrate.Index{{Name: "IX_label", Columns: []migrate.IndexColumn{{Name: "label"}}}}
		artifact := planArtifact(completeRequest(current, desired))
		marked := map[string]bool{}
		for _, u := range artifact.Units {
			for _, op := range u.Ops {
				for _, s := range op.Statements {
					marked[s.SQL] = s.SizeOfData
				}
			}
		}
		Expect(marked).To(Equal(map[string]bool{
			"ALTER TABLE [dbo].[orders] ALTER COLUMN [id] bigint NOT NULL":     true,
			"ALTER TABLE [dbo].[orders] ADD [note] int NULL":                   false,
			"CREATE NONCLUSTERED INDEX [IX_label] ON [dbo].[orders] ([label])": true,
		}))
	})
})
