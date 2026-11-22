package sqlserver_test

import (
	"strings"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func stepSQL(plan migrate.Plan) []string {
	sql := make([]string, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		sql = append(sql, step.SQL)
	}
	return sql
}

var _ = Describe("Fill factor, ROWGUIDCOL, QUOTED_IDENTIFIER and sequences", func() {
	orders := migrate.Name{Schema: "dbo", Name: "orders"}
	keyed := func(pkFill, indexFill int) migrate.Table {
		return migrate.Table{Name: orders, Columns: []migrate.Column{{Name: "id", Type: "int"}, {Name: "code", Type: "nvarchar(20)"}},
			Indexes: []migrate.Index{
				{Name: "PK_orders", Primary: true, Clustered: true, Columns: []migrate.IndexColumn{{Name: "id"}}, FillFactor: pkFill},
				{Name: "IX_orders_code", Columns: []migrate.IndexColumn{{Name: "code"}}, FillFactor: indexFill},
			}}
	}

	It("creates keys and indexes with their fill factor", func() {
		plan, err := sqlserver.Plan(migrate.Request{Desired: migrate.Schema{Tables: []migrate.Table{keyed(90, 80)}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(stepSQL(plan)).To(Equal([]string{
			"CREATE TABLE [dbo].[orders] ([id] int NOT NULL, [code] nvarchar(20) NOT NULL, CONSTRAINT [PK_orders] PRIMARY KEY CLUSTERED ([id]) WITH (FILLFACTOR = 90))",
			"CREATE NONCLUSTERED INDEX [IX_orders_code] ON [dbo].[orders] ([code]) WITH (FILLFACTOR = 80)",
		}))
	})

	It("treats FILLFACTOR = 100 as the server default", func() {
		plan, err := sqlserver.Plan(migrate.Request{Current: migrate.Schema{Tables: []migrate.Table{keyed(0, 0)}}, Desired: migrate.Schema{Tables: []migrate.Table{keyed(100, 100)}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Steps).To(BeEmpty())
	})

	It("rebuilds an index in place when only its fill factor changes", func() {
		plan, err := sqlserver.Plan(migrate.Request{Current: migrate.Schema{Tables: []migrate.Table{keyed(90, 80)}}, Desired: migrate.Schema{Tables: []migrate.Table{keyed(0, 70)}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(stepSQL(plan)).To(ConsistOf(
			"ALTER INDEX [PK_orders] ON [dbo].[orders] REBUILD WITH (FILLFACTOR = 100)",
			"ALTER INDEX [IX_orders_code] ON [dbo].[orders] REBUILD WITH (FILLFACTOR = 70)",
		))
		for _, change := range plan.Changes {
			Expect(change.Destructive).To(BeFalse())
		}
	})

	It("rejects a fill factor outside 0-100", func() {
		_, err := sqlserver.Plan(migrate.Request{Desired: migrate.Schema{Tables: []migrate.Table{keyed(0, 101)}}})
		Expect(err).To(MatchError(ContainSubstring("fill factor 101")))
	})

	guid := func(rowGUID bool) migrate.Table {
		return migrate.Table{Name: orders, Columns: []migrate.Column{{Name: "id", Type: "uniqueidentifier", RowGUID: rowGUID, Default: &migrate.Default{Name: "DF_orders_id", Expression: "(newid())"}}}}
	}

	It("creates, adds and drops ROWGUIDCOL", func() {
		plan, err := sqlserver.Plan(migrate.Request{Desired: migrate.Schema{Tables: []migrate.Table{guid(true)}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(stepSQL(plan)).To(Equal([]string{"CREATE TABLE [dbo].[orders] ([id] uniqueidentifier NOT NULL ROWGUIDCOL CONSTRAINT [DF_orders_id] DEFAULT (newid()))"}))
		plan, err = sqlserver.Plan(migrate.Request{Current: migrate.Schema{Tables: []migrate.Table{guid(false)}}, Desired: migrate.Schema{Tables: []migrate.Table{guid(true)}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(stepSQL(plan)).To(Equal([]string{"ALTER TABLE [dbo].[orders] ALTER COLUMN [id] ADD ROWGUIDCOL"}))
		Expect(plan.Changes[0].Destructive).To(BeFalse())
		plan, err = sqlserver.Plan(migrate.Request{Current: migrate.Schema{Tables: []migrate.Table{guid(true)}}, Desired: migrate.Schema{Tables: []migrate.Table{guid(false)}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(stepSQL(plan)).To(Equal([]string{"ALTER TABLE [dbo].[orders] ALTER COLUMN [id] DROP ROWGUIDCOL"}))
	})

	It("rejects ROWGUIDCOL on a non-uniqueidentifier column", func() {
		table := migrate.Table{Name: orders, Columns: []migrate.Column{{Name: "id", Type: "int", RowGUID: true}}}
		_, err := sqlserver.Plan(migrate.Request{Desired: migrate.Schema{Tables: []migrate.Table{table}}})
		Expect(err).To(MatchError(ContainSubstring("ROWGUIDCOL requires uniqueidentifier")))
	})

	proc := func(quotedIdentifierOff bool) migrate.Definition {
		return migrate.Definition{Name: migrate.Name{Schema: "dbo", Name: "greet"}, Kind: "PROCEDURE", QuotedIdentifierOff: quotedIdentifierOff,
			SQL: `CREATE PROCEDURE dbo.greet AS SELECT "it's"`}
	}

	It("creates a QUOTED_IDENTIFIER OFF module inside a nested batch", func() {
		plan, err := sqlserver.Plan(migrate.Request{Desired: migrate.Schema{Definitions: []migrate.Definition{proc(true)}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(stepSQL(plan)).To(Equal([]string{"SET QUOTED_IDENTIFIER OFF;\nEXEC sys.sp_executesql N'CREATE OR ALTER PROCEDURE dbo.greet AS SELECT \"it''s\"';\nSET QUOTED_IDENTIFIER ON;"}))
	})

	It("re-creates a module whose only change is its QUOTED_IDENTIFIER setting", func() {
		plan, err := sqlserver.Plan(migrate.Request{Current: migrate.Schema{Definitions: []migrate.Definition{proc(false)}}, Desired: migrate.Schema{Definitions: []migrate.Definition{proc(true)}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Changes).To(HaveLen(1))
		Expect([]string{plan.Changes[0].Action, plan.Changes[0].Kind}).To(Equal([]string{"modify", "procedure"}))
		unchanged, err := sqlserver.Plan(migrate.Request{Current: migrate.Schema{Definitions: []migrate.Definition{proc(true)}}, Desired: migrate.Schema{Definitions: []migrate.Definition{proc(true)}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(unchanged.Steps).To(BeEmpty())
	})

	It("replays an unqualified module into its catalog schema", func() {
		unqualified := migrate.Definition{Name: migrate.Name{Schema: "dbo", Name: "greet"}, Kind: "PROCEDURE", SQL: "CREATE PROCEDURE greet AS SELECT 1"}
		plan, err := sqlserver.Plan(migrate.Request{Desired: migrate.Schema{Definitions: []migrate.Definition{unqualified}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(stepSQL(plan)).To(Equal([]string{"CREATE OR ALTER PROCEDURE [dbo].greet AS SELECT 1"}))
		unchanged, err := sqlserver.Plan(migrate.Request{Current: migrate.Schema{Definitions: []migrate.Definition{unqualified}}, Desired: migrate.Schema{Definitions: []migrate.Definition{unqualified}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(unchanged.Steps).To(BeEmpty())
	})

	Describe("a definition whose body is not understood", func() {
		const header, body = "CREATE PROCEDURE dbo.acme_purge AS ", "DELETE TOP (500) a FROM dbo.acme a; RAISERROR(N'it''s %d', 0, 1, 2) WITH NOWAIT;"
		purge := func(sql string) migrate.Schema {
			return migrate.Schema{Definitions: []migrate.Definition{{Name: migrate.Name{Schema: "dbo", Name: "acme_purge"}, Kind: "PROCEDURE", SQL: sql}}}
		}
		warning := ContainSubstring("procedure dbo.acme_purge is compared as written")

		DescribeTable("is compared as written, with a warning",
			func(current, desired string, steps ...string) {
				plan, err := sqlserver.Plan(migrate.Request{Current: purge(current), Desired: purge(desired)})
				Expect(err).NotTo(HaveOccurred())
				Expect(stepSQL(plan)).To(Equal(append([]string{}, steps...)))
				Expect(plan.Warnings).To(ConsistOf(warning))
			},
			Entry("the same text", header+body, header+body),
			Entry("another layout, comment and OR ALTER", header+body, "-- note\nCREATE OR ALTER PROCEDURE dbo.acme_purge AS\n\t/* purge */ "+body),
			Entry("an unqualified name", header+body, "CREATE PROCEDURE acme_purge AS "+body),
			Entry("another literal", header+body, header+strings.Replace(body, "it''s", "its", 1), "CREATE OR ALTER PROCEDURE dbo.acme_purge AS "+strings.Replace(body, "it''s", "its", 1)),
			Entry("a body that is understood", header+body, header+"SELECT 1", "CREATE OR ALTER PROCEDURE dbo.acme_purge AS SELECT 1"),
			Entry("one that was understood", header+"SELECT 1", header+body, "CREATE OR ALTER PROCEDURE dbo.acme_purge AS "+body),
		)

		It("is created as written", func() {
			plan, err := sqlserver.Plan(migrate.Request{Desired: purge("CREATE PROCEDURE [dbo].[acme_purge] AS " + body)})
			Expect(err).NotTo(HaveOccurred())
			Expect(stepSQL(plan)).To(Equal([]string{"CREATE OR ALTER PROCEDURE [dbo].[acme_purge] AS " + body}))
		})

		It("is kept for a function, a view and a trigger too", func() {
			orders := migrate.Table{Name: migrate.Name{Schema: "dbo", Name: "acme"}, Columns: []migrate.Column{{Name: "id", Type: "int"}}}
			named := func(kind, name, sql string) migrate.Definition {
				return migrate.Definition{Name: migrate.Name{Schema: "dbo", Name: name}, Kind: kind, SQL: sql}
			}
			trigger := named("TRIGGER", "acme_tr", "CREATE TRIGGER dbo.acme_tr ON dbo.acme AFTER INSERT AS "+body)
			trigger.Parent = &orders.Name
			snapshot := migrate.Schema{Tables: []migrate.Table{orders}, Definitions: []migrate.Definition{
				named("FUNCTION", "acme_fn", "CREATE FUNCTION dbo.acme_fn() RETURNS int AS BEGIN SELECT 1 + ; RETURN 1 END"),
				named("VIEW", "acme_v", "CREATE VIEW dbo.acme_v AS SELECT 1 +"), trigger}}
			plan, err := sqlserver.Plan(migrate.Request{Current: snapshot, Desired: snapshot})
			Expect(err).NotTo(HaveOccurred())
			Expect(plan.Steps).To(BeEmpty())
			Expect(plan.Warnings).To(ConsistOf(ContainSubstring("function dbo.acme_fn"), ContainSubstring("view dbo.acme_v"), ContainSubstring("trigger dbo.acme_tr")))
		})

		It("is still refused when its header does not name the catalog's object", func() {
			_, err := sqlserver.Plan(migrate.Request{Current: purge("CREATE PROCEDURE dbo.other AS " + body)})
			Expect(err).To(MatchError(ContainSubstring("definition name or kind does not match")))
		})

		It("warns about nothing it understands", func() {
			plan, err := sqlserver.Plan(migrate.Request{Current: purge(header + "SELECT 1")})
			Expect(err).NotTo(HaveOccurred())
			Expect(plan.Warnings).To(BeEmpty())
		})
	})

	balance := func(name, expression string) migrate.Table {
		return migrate.Table{Name: orders, Columns: []migrate.Column{{Name: "balance", Type: "int", Default: &migrate.Default{Name: name, Expression: expression, SystemNamed: true}}}}
	}

	It("ignores the name of a system-named default and creates new ones unnamed", func() {
		plan, err := sqlserver.Plan(migrate.Request{Current: migrate.Schema{Tables: []migrate.Table{balance("DF__orders__balance__1A2B3C4D", "((0))")}},
			Desired: migrate.Schema{Tables: []migrate.Table{balance("DF__orders__balance__5E6F7A8B", "((0))")}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Steps).To(BeEmpty())
		plan, err = sqlserver.Plan(migrate.Request{Current: migrate.Schema{Tables: []migrate.Table{balance("DF__orders__balance__1A2B3C4D", "((0))")}},
			Desired: migrate.Schema{Tables: []migrate.Table{balance("DF__orders__balance__5E6F7A8B", "((1))")}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(stepSQL(plan)).To(Equal([]string{
			"ALTER TABLE [dbo].[orders] DROP CONSTRAINT [DF__orders__balance__1A2B3C4D]",
			"ALTER TABLE [dbo].[orders] ADD DEFAULT ((1)) FOR [balance]",
		}))
		plan, err = sqlserver.Plan(migrate.Request{Desired: migrate.Schema{Tables: []migrate.Table{balance("DF__orders__balance__5E6F7A8B", "((0))")}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(stepSQL(plan)).To(Equal([]string{"CREATE TABLE [dbo].[orders] ([balance] int NOT NULL DEFAULT ((0)))"}))
	})

	fieldList := migrate.TableType{Name: migrate.Name{Schema: "dbo", Name: "field_list"},
		Columns: []migrate.Column{
			{Name: "id", Type: "int", Identity: &migrate.Identity{Seed: 1, Increment: 1}},
			{Name: "name", Type: "nvarchar(50)", Collation: "Latin1_General_CI_AS"},
			{Name: "kind", Type: "nvarchar(30)", Nullable: true, Default: &migrate.Default{Expression: "(N'text')"}},
		},
		Keys:   []migrate.Index{{Primary: true, Clustered: true, Columns: []migrate.IndexColumn{{Name: "id"}}}, {Unique: true, Columns: []migrate.IndexColumn{{Name: "name"}}}},
		Checks: []string{"([kind]<>N'')"},
	}
	usesFieldList := migrate.Definition{Name: migrate.Name{Schema: "dbo", Name: "load_fields"}, Kind: "PROCEDURE",
		SQL: "CREATE PROCEDURE dbo.load_fields @fields dbo.field_list READONLY AS SELECT name FROM @fields"}

	It("reads back a live procedure taking a mixed-case table type with a sql_variant column", func() {
		fieldValues := migrate.TableType{Name: migrate.Name{Schema: "dbo", Name: "FieldValueList"},
			Columns: []migrate.Column{
				{Name: "Name", Type: "nvarchar(50)"},
				{Name: "Value", Type: "sql_variant", Nullable: true},
			}}
		spawn := migrate.Definition{Name: migrate.Name{Schema: "dbo", Name: "SpawnForGroup"}, Kind: "PROCEDURE",
			SQL: "-- Spawns one row per member.\n--   EXEC dbo.SpawnForGroup @GroupGUID=[GroupGUID], @Fields=@f\n" +
				"CREATE   PROCEDURE dbo.SpawnForGroup\n    @GroupGUID UNIQUEIDENTIFIER,\n    @Fields    dbo.FieldValueList READONLY\n" +
				"AS\nBEGIN\n    SET NOCOUNT ON;\n    SELECT Name FROM @Fields;\nEND\n"}
		live := migrate.Schema{TableTypes: []migrate.TableType{fieldValues}, Definitions: []migrate.Definition{spawn}}
		plan, err := sqlserver.Plan(migrate.Request{Current: live, Desired: live})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Steps).To(BeEmpty())
	})

	It("creates a table type before the procedure that takes it", func() {
		plan, err := sqlserver.Plan(migrate.Request{Desired: migrate.Schema{TableTypes: []migrate.TableType{fieldList}, Definitions: []migrate.Definition{usesFieldList}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(stepSQL(plan)).To(Equal([]string{
			"CREATE TYPE [dbo].[field_list] AS TABLE ([id] int IDENTITY(1,1) NOT NULL, [name] nvarchar(50) COLLATE Latin1_General_CI_AS NOT NULL, [kind] nvarchar(30) NULL DEFAULT (N'text'), PRIMARY KEY CLUSTERED ([id]), UNIQUE NONCLUSTERED ([name]), CHECK (([kind]<>N'')))",
			"CREATE OR ALTER PROCEDURE dbo.load_fields @fields dbo.field_list READONLY AS SELECT name FROM @fields",
		}))
		Expect([]string{plan.Changes[0].Action, plan.Changes[0].Kind, plan.Changes[0].Object.Name}).To(Equal([]string{"add", "table-type", "field_list"}))
	})

	It("plans nothing for an identical table type, refuses a changed one and drops a removed one", func() {
		current := migrate.Schema{TableTypes: []migrate.TableType{fieldList}}
		plan, err := sqlserver.Plan(migrate.Request{Current: current, Desired: current})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Steps).To(BeEmpty())
		changed := fieldList
		changed.Checks = []string{"([kind]<>N'none')"}
		_, err = sqlserver.Plan(migrate.Request{Current: current, Desired: migrate.Schema{TableTypes: []migrate.TableType{changed}}})
		Expect(err).To(MatchError(ContainSubstring("changing a table type")))
		plan, err = sqlserver.Plan(migrate.Request{Current: current})
		Expect(err).NotTo(HaveOccurred())
		Expect(stepSQL(plan)).To(Equal([]string{"DROP TYPE [dbo].[field_list]"}))
		Expect(plan.Changes[0].Destructive).To(BeTrue())
	})

	It("refuses a parameter whose user-defined type is not declared", func() {
		_, err := sqlserver.Plan(migrate.Request{Desired: migrate.Schema{Definitions: []migrate.Definition{usesFieldList}}})
		Expect(err).To(MatchError(ContainSubstring("parameter @fields")))
	})

	ids := migrate.Sequence{Name: migrate.Name{Schema: "dbo", Name: "order_ids"}, Type: "bigint", Start: "1", Increment: "1",
		Minimum: "-9223372036854775808", Maximum: "9223372036854775807"}

	It("creates, alters and drops sequences", func() {
		plan, err := sqlserver.Plan(migrate.Request{Desired: migrate.Schema{Sequences: []migrate.Sequence{ids}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(stepSQL(plan)).To(Equal([]string{"CREATE SEQUENCE [dbo].[order_ids] AS bigint START WITH 1 INCREMENT BY 1 MINVALUE -9223372036854775808 MAXVALUE 9223372036854775807 NO CYCLE CACHE"}))
		Expect([]string{plan.Changes[0].Action, plan.Changes[0].Kind, plan.Changes[0].Object.Name}).To(Equal([]string{"add", "sequence", "order_ids"}))
		altered := ids
		altered.Increment, altered.Cycle, altered.CacheSize = "5", true, 50
		plan, err = sqlserver.Plan(migrate.Request{Current: migrate.Schema{Sequences: []migrate.Sequence{ids}}, Desired: migrate.Schema{Sequences: []migrate.Sequence{altered}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(stepSQL(plan)).To(Equal([]string{"ALTER SEQUENCE [dbo].[order_ids] INCREMENT BY 5 MINVALUE -9223372036854775808 MAXVALUE 9223372036854775807 CYCLE CACHE 50"}))
		plan, err = sqlserver.Plan(migrate.Request{Current: migrate.Schema{Sequences: []migrate.Sequence{ids}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(stepSQL(plan)).To(Equal([]string{"DROP SEQUENCE [dbo].[order_ids]"}))
		Expect(plan.Changes[0].Destructive).To(BeTrue())
	})

	It("plans nothing for identical sequences and refuses START WITH changes", func() {
		plan, err := sqlserver.Plan(migrate.Request{Current: migrate.Schema{Sequences: []migrate.Sequence{ids}}, Desired: migrate.Schema{Sequences: []migrate.Sequence{ids}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Steps).To(BeEmpty())
		restarted := ids
		restarted.Start = "1000"
		_, err = sqlserver.Plan(migrate.Request{Current: migrate.Schema{Sequences: []migrate.Sequence{ids}}, Desired: migrate.Schema{Sequences: []migrate.Sequence{restarted}}})
		Expect(err).To(MatchError(ContainSubstring("START WITH")))
	})

	It("rejects non-integer sequence bounds", func() {
		invalid := ids
		invalid.Maximum = "1e9"
		_, err := sqlserver.Plan(migrate.Request{Desired: migrate.Schema{Sequences: []migrate.Sequence{invalid}}})
		Expect(err).To(MatchError(ContainSubstring(`maximum "1e9"`)))
	})
})
