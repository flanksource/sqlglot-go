package sqlserver_test

import (
	"regexp"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	"github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The pure planner is the whole "sqlserver" dialect for rendering: this test
// binary cannot link the database half, which imports this package.
func init() { engine.Register(sqlserver.Planner{}) }

func scriptRequest(current, desired migrate.Schema) engine.PlanRequest {
	request := completeRequest(current, desired)
	request.Dialect = "sqlserver"
	return request
}

func renderScript(a engine.Artifact) string {
	GinkgoHelper()
	script, err := sqlserver.RenderScript(a, sqlserver.ScriptOptions{Acknowledgements: engine.Acknowledgements(a)})
	Expect(err).NotTo(HaveOccurred())
	return script
}

func renderBetween(current, desired migrate.Schema) string {
	GinkgoHelper()
	return renderScript(planArtifact(scriptRequest(current, desired)))
}

var _ = Describe("Rendering an artifact as a script", func() {
	retype := func() engine.Artifact {
		request := scriptRequest(guidSchema("varchar(36)", "varchar(36)"), guidSchema("uniqueidentifier", "uniqueidentifier"))
		request.Dependents = liveDependents()
		return planArtifact(request)
	}

	It("renders a unit as a guarded transaction: lock, state guard, preconditions, then statements in plan order", func() {
		a := retype()
		Expect(a.Units).To(HaveLen(1))
		script := renderScript(a)
		unit := a.Units[0]

		Expect(script).To(HavePrefix("-- sqlglot SQL Server migration artifact " + a.Hash + "\n"))
		Expect(script).To(ContainSubstring("DECLARE @options int = @@OPTIONS;\n" + sqlserver.SessionOptions + "\n"))
		Expect(script).To(ContainSubstring("-- unit " + unit.ID + ": "))
		Expect(script).To(ContainSubstring("DECLARE @u1 bit = 0;\nBEGIN TRANSACTION;\n"))
		Expect(script).To(ContainSubstring("@LockMode = 'Exclusive', @LockOwner = 'Transaction'"))
		Expect(script).To(ContainSubstring("ELSE IF NOT ("))
		Expect(script).To(ContainSubstring("matches neither its baseline nor its desired state', 1;"))
		Expect(script).To(HaveSuffix("COMMIT TRANSACTION;\n" + restoreOptions))
		Expect(strings.Count(script, "BEGIN TRANSACTION;")).To(Equal(1))

		Expect(unit.Preconditions).NotTo(BeEmpty())
		for _, p := range unit.Preconditions {
			Expect(script).To(ContainSubstring("IF @u1 = 1 AND EXISTS (" + p.SQL + ") THROW 50000, N'" + p.Message + "', 1;\n"))
		}
		ordered := []string{
			"IF @u1 = 1 AND EXISTS (",
			"IF @u1 = 1 EXEC sys.sp_executesql N'DROP INDEX [IX_accounts_id] ON [dbo].[accounts]';\n",
			"EXEC sys.sp_executesql N'ALTER TABLE [dbo].[accounts] ALTER COLUMN [id] uniqueidentifier NOT NULL';\n",
			"IF @u1 = 1 EXEC sys.sp_executesql N'CREATE NONCLUSTERED INDEX [IX_accounts_id] ON [dbo].[accounts] ([id])",
		}
		last := -1
		for _, fragment := range ordered {
			at := strings.Index(script, fragment)
			Expect(at).To(BeNumerically(">", last), fragment)
			last = at
		}
	})

	It("drops the column's baseline default under the name the database gives it when the script runs", func() {
		script := renderScript(retype())
		Expect(script).NotTo(ContainSubstring("DROP CONSTRAINT [DF_accounts_id]"))
		Expect(script).To(ContainSubstring("IF @u1 = 1\nBEGIN\nSET @df = NULL;\nSELECT @df = dc.name FROM sys.default_constraints AS dc"))
	})

	It("stays valid at compatibility level 100: no batch separators, no DROP IF EXISTS, no CREATE OR ALTER", func() {
		script := renderScript(retype())
		Expect(regexp.MustCompile(`(?mi)^\s*GO\s*$`).MatchString(script)).To(BeFalse())
		Expect(strings.ToUpper(script)).NotTo(ContainSubstring("DROP INDEX IF EXISTS"))
		Expect(strings.ToUpper(script)).NotTo(ContainSubstring("CREATE OR ALTER"))
	})

	It("renders the same artifact to the same script every time", func() {
		a := retype()
		Expect(renderScript(a)).To(Equal(renderScript(a)))
	})

	It("renders one transaction per independent unit, each under its own flag", func() {
		withTable := func(name string, columnType string) migrate.Table {
			return migrate.Table{Name: migrate.Name{Schema: "dbo", Name: name}, Columns: []migrate.Column{{Name: "v", Type: columnType, Nullable: true}}}
		}
		current := schemaOf(withTable("one", "int"), withTable("two", "int"))
		desired := schemaOf(withTable("one", "bigint"), withTable("two", "bigint"))
		a := planArtifact(scriptRequest(current, desired))
		Expect(a.Units).To(HaveLen(2))
		script := renderScript(a)
		Expect(strings.Count(script, "BEGIN TRANSACTION;")).To(Equal(2))
		Expect(strings.Count(script, "COMMIT TRANSACTION;")).To(Equal(2))
		Expect(script).To(ContainSubstring("DECLARE @u1 bit = 0;"))
		Expect(script).To(ContainSubstring("DECLARE @u2 bit = 0;"))
		Expect(strings.Index(script, "-- unit "+a.Units[0].ID)).To(BeNumerically("<", strings.Index(script, "-- unit "+a.Units[1].ID)))
	})

	It("refuses destructive changes that are not acknowledged exactly, and artifacts that are not for SQL Server or were edited", func() {
		current := schemaOf(charTable(charOrders, charColumn("id", "int", false), charColumn("note", "nvarchar(50)", true)))
		desired := schemaOf(charTable(charOrders, charColumn("id", "int", false)))
		a := planArtifact(scriptRequest(current, desired))
		drop := engine.Acknowledgements(a)
		Expect(drop).To(HaveLen(1))
		_, err := sqlserver.RenderScript(a, sqlserver.ScriptOptions{})
		Expect(err).To(MatchError(ContainSubstring("requires acknowledgements")))
		_, err = sqlserver.RenderScript(a, sqlserver.ScriptOptions{Acknowledgements: append(drop, "unknown")})
		Expect(err).To(MatchError(ContainSubstring("unknown or duplicate destructive acknowledgement")))

		edited := a
		edited.Units = append([]engine.Unit(nil), a.Units...)
		edited.Units[0].Ops = append([]engine.Op(nil), a.Units[0].Ops...)
		edited.Units[0].Ops[0].Statements = []engine.Statement{{SQL: "DROP TABLE [dbo].[orders]"}}
		_, err = sqlserver.RenderScript(edited, sqlserver.ScriptOptions{Acknowledgements: drop})
		Expect(err).To(MatchError(ContainSubstring("hash mismatch")))

		other := a
		other.Dialect = "postgres"
		_, err = sqlserver.RenderScript(other, sqlserver.ScriptOptions{Acknowledgements: drop})
		Expect(err).To(MatchError(ContainSubstring("cannot render a postgres artifact")))
	})
})

var _ = Describe("The state guard of each kind of change", func() {
	label := migrate.Column{Name: "label", Type: "nvarchar(20)", Nullable: true, Collation: "Latin1_General_CI_AS"}
	table := func(columns ...migrate.Column) migrate.Table {
		return charTable(charOrders, append([]migrate.Column{charColumn("id", "int", false)}, columns...)...)
	}

	It("adds a member only when absent, skips it in its exact desired shape and throws on any other shape", func() {
		desired := table(label, migrate.Column{Name: "total", Type: "int", Nullable: true, Default: &migrate.Default{Name: "DF_orders_total", Expression: "((0))"}})
		desired.Indexes = []migrate.Index{{Name: "IX_orders_label", Columns: []migrate.IndexColumn{{Name: "label"}}, Filter: "([label] IS NOT NULL)"}}
		desired.Checks = []migrate.Check{{Name: "CK_orders_total", Expression: "([total]>=(0))"}}
		script := renderBetween(schemaOf(table(label)), schemaOf(desired))
		for _, guard := range []string{
			"COL_LENGTH(N'[dbo].[orders]', N'total') IS NULL",
			"OBJECT_DEFINITION(c.default_object_id) = N'((0))'",
			"NOT EXISTS (SELECT 1 FROM sys.indexes WHERE object_id = OBJECT_ID(N'[dbo].[orders]') AND name = N'IX_orders_label')",
			"i.filter_definition = N'([label] IS NOT NULL)'",
			"NOT EXISTS (SELECT 1 FROM sys.check_constraints WHERE parent_object_id = OBJECT_ID(N'[dbo].[orders]') AND name = N'CK_orders_total')",
			"k.definition = N'([total]>=(0))'",
			"matches neither its baseline nor its desired state",
		} {
			Expect(script).To(ContainSubstring(guard))
		}
	})

	It("drops a member or an object only while it exists", func() {
		wide := table(label)
		wide.Indexes = []migrate.Index{{Name: "IX_orders_label", Columns: []migrate.IndexColumn{{Name: "label"}}}}
		dropped := renderBetween(schemaOf(wide), schemaOf(table()))
		Expect(dropped).To(ContainSubstring("COL_LENGTH(N'[dbo].[orders]', N'label') IS NOT NULL"))
		Expect(dropped).To(ContainSubstring("EXISTS (SELECT 1 FROM sys.indexes WHERE object_id = OBJECT_ID(N'[dbo].[orders]') AND name = N'IX_orders_label')"))
		Expect(renderBetween(schemaOf(wide), migrate.Schema{})).To(ContainSubstring("OBJECT_ID(N'[dbo].[orders]', N'U') IS NOT NULL"))
	})

	It("guards a foreign key on its parent table", func() {
		other := charTable(migrate.Name{Schema: "dbo", Name: "items"}, charColumn("id", "int", false))
		keyed := other
		keyed.ForeignKeys = []migrate.ForeignKey{{Name: "FK_items_orders", Columns: []string{"id"}, References: charOrders, ReferencedColumns: []string{"id"}}}
		script := renderBetween(schemaOf(table(), other), schemaOf(table(), keyed))
		Expect(script).To(ContainSubstring("NOT EXISTS (SELECT 1 FROM sys.foreign_keys WHERE parent_object_id = OBJECT_ID(N'[dbo].[items]') AND name = N'FK_items_orders')"))
		Expect(script).To(ContainSubstring("f.referenced_object_id = OBJECT_ID(N'[dbo].[orders]')"))
	})

	It("alters a column only from its exact baseline, skips its exact desired state and throws on anything else", func() {
		wider := label
		wider.Type = "nvarchar(100)"
		wider.Default = &migrate.Default{Name: "DF_orders_label", Expression: "(N'open')"}
		script := renderBetween(schemaOf(table(label)), schemaOf(table(wider)))
		Expect(script).To(ContainSubstring("= N'nvarchar(20)' AND c.is_nullable = 1 AND c.is_rowguidcol = 0 AND c.is_identity = 0 AND c.collation_name = N'Latin1_General_CI_AS' AND c.default_object_id = 0"))
		Expect(script).To(ContainSubstring("= N'nvarchar(100)' AND c.is_nullable = 1 AND c.is_rowguidcol = 0 AND c.is_identity = 0 AND c.collation_name = N'Latin1_General_CI_AS' AND OBJECT_DEFINITION(c.default_object_id) = N'(N''open'')'"))
		Expect(script).To(ContainSubstring("matches neither its baseline nor its desired state', 1;\n"))
	})

	It("drops a column's default under the name the database gives it when the script runs", func() {
		defaulted := label
		defaulted.Default = &migrate.Default{Name: "DF_orders_label", Expression: "(N'pending')"}
		wider := defaulted
		wider.Type = "nvarchar(100)"
		for _, desired := range []migrate.Schema{schemaOf(table(wider)), schemaOf(table())} {
			script := renderBetween(schemaOf(table(defaulted)), desired)
			Expect(script).NotTo(ContainSubstring("DROP CONSTRAINT [DF_orders_label]"))
			Expect(script).To(ContainSubstring("SELECT @df = dc.name FROM sys.default_constraints AS dc JOIN sys.columns AS c ON c.object_id = dc.parent_object_id AND c.column_id = dc.parent_column_id WHERE c.object_id = OBJECT_ID(N'[dbo].[orders]') AND c.name = N'label';\n" +
				"IF @df IS NOT NULL\nBEGIN\nSET @sql = N'ALTER TABLE [dbo].[orders] DROP CONSTRAINT ' + QUOTENAME(@df);\nEXEC sys.sp_executesql @sql;\nEND\nEND\n"))
		}
	})

	It("refuses to drop a constraint SQL Server named itself, naming the change", func() {
		other := charTable(migrate.Name{Schema: "dbo", Name: "items"}, charColumn("id", "int", false))
		keyed := other
		keyed.ForeignKeys = []migrate.ForeignKey{{Name: "FK__items__id__5EBF139D", Columns: []string{"id"}, References: charOrders, ReferencedColumns: []string{"id"}}}
		a := planArtifact(scriptRequest(schemaOf(table(), keyed), schemaOf(table(), other)))
		_, err := sqlserver.RenderScript(a, sqlserver.ScriptOptions{Acknowledgements: engine.Acknowledgements(a)})
		Expect(err).To(MatchError(And(ContainSubstring(changeID("drop", "foreign-key", "items", "FK__items__id__5EBF139D")), ContainSubstring("system-named"))))
	})

	It("modifies a check from its exact baseline definition and skips only its exact desired definition", func() {
		from, to := table(), table()
		from.Checks = []migrate.Check{{Name: "CK_orders_id", Expression: "([id]>(0))"}}
		to.Checks = []migrate.Check{{Name: "CK_orders_id", Expression: "([id]>(1))"}}
		script := renderBetween(schemaOf(from), schemaOf(to))
		Expect(script).To(ContainSubstring("k.name = N'CK_orders_id' AND k.definition = N'([id]>(0))'"))
		Expect(script).To(ContainSubstring("k.name = N'CK_orders_id' AND k.definition = N'([id]>(1))'"))
	})

	It("runs CREATE OR ALTER modules unconditionally and guards module drops", func() {
		view := migrate.Definition{Name: migrate.Name{Schema: "dbo", Name: "order_ids"}, Kind: "VIEW", SQL: "CREATE VIEW dbo.order_ids AS SELECT N'x' AS id"}
		withView := migrate.Schema{Definitions: []migrate.Definition{view}}
		Expect(renderBetween(migrate.Schema{}, withView)).To(ContainSubstring("SET @u1 = 1;\nIF @u1 = 1 EXEC sys.sp_executesql N'CREATE OR ALTER VIEW dbo.order_ids AS SELECT N''x'' AS id';\n"))
		Expect(renderBetween(withView, migrate.Schema{})).To(ContainSubstring("OBJECT_ID(N'[dbo].[order_ids]', N'V') IS NOT NULL"))
	})

	It("escapes brackets in identifiers and quotes in literals", func() {
		odd := charTable(migrate.Name{Schema: "dbo", Name: "o]r'ders"}, charColumn("id", "int", false))
		script := renderBetween(migrate.Schema{}, schemaOf(odd))
		Expect(script).To(ContainSubstring("OBJECT_ID(N'[dbo].[o]]r''ders]', N'U') IS NULL"))
		Expect(script).To(ContainSubstring("EXEC sys.sp_executesql N'CREATE TABLE [dbo].[o]]r''ders] ([id] int NOT NULL)';"))
	})
})

// restoreOptions is the closing restore of the session options.
const restoreOptions = "IF @options & 16384 = 0 SET XACT_ABORT OFF;\n" +
	"IF @options & 32 = 0 SET ANSI_NULLS OFF;\n" +
	"IF @options & 16 = 0 SET ANSI_PADDING OFF;\n" +
	"IF @options & 8 = 0 SET ANSI_WARNINGS OFF;\n" +
	"IF @options & 64 = 0 SET ARITHABORT OFF;\n" +
	"IF @options & 4096 = 0 SET CONCAT_NULL_YIELDS_NULL OFF;\n" +
	"IF @options & 8192 <> 0 SET NUMERIC_ROUNDABORT ON;\n"
