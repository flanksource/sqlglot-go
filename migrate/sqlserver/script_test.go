package sqlserver

import (
	"regexp"
	"strings"

	"github.com/flanksource/sqlglot-go/migrate"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var scriptOrders = migrate.Name{Schema: "dbo", Name: "orders"}

func scriptTable(columns ...migrate.Column) migrate.Table {
	return migrate.Table{Name: scriptOrders, Columns: append([]migrate.Column{{Name: "id", Type: "int"}}, columns...)}
}

func scriptArtifact(current, desired migrate.Schema) migrate.Artifact {
	artifact, err := BuildArtifact(migrate.ArtifactRequest{Schemas: []string{"dbo"}, Request: migrate.Request{Current: current, Desired: desired}})
	Expect(err).NotTo(HaveOccurred())
	return artifact
}

func tables(t ...migrate.Table) migrate.Schema { return migrate.Schema{Tables: t} }

func renderAcknowledged(artifact migrate.Artifact) string {
	script, err := RenderScript(artifact, ScriptOptions{Acknowledgements: Acknowledgements(artifact)})
	Expect(err).NotTo(HaveOccurred())
	return script
}

var _ = Describe("Post-deployment scripts", func() {
	label := migrate.Column{Name: "label", Type: "nvarchar(20)", Nullable: true, Collation: "Latin1_General_CI_AS"}

	It("renders one transaction under the migration applock with no batch separators", func() {
		artifact := scriptArtifact(migrate.Schema{}, tables(scriptTable()))
		script := renderAcknowledged(artifact)
		Expect(script).To(HavePrefix("-- sqlglot SQL Server migration artifact " + artifact.Hash + "\n"))
		Expect(script).To(ContainSubstring("Nothing verifies the hash when the script runs"))
		Expect(script).NotTo(ContainSubstring("invalidates the artifact hash"))
		Expect(script).To(ContainSubstring(`-- Schemas: ["dbo"]`))
		Expect(script).To(ContainSubstring(`--   ["add","table","dbo","orders",""]`))
		Expect(script).To(ContainSubstring("DECLARE @options int = @@OPTIONS;\n" + SessionOptions + "\nBEGIN TRANSACTION;\n"))
		Expect(script).To(ContainSubstring("EXEC @lock = sys.sp_getapplock @Resource = N'commons-db/migrate/schema', @LockMode = 'Exclusive', @LockOwner = 'Transaction', @LockTimeout = 30000;\nIF @lock < 0 THROW"))
		Expect(strings.Count(script, "BEGIN TRANSACTION;")).To(Equal(1))
		Expect(strings.Count(script, "COMMIT TRANSACTION;")).To(Equal(1))
		Expect(script).To(HaveSuffix("COMMIT TRANSACTION;\n" + restoreSessionOptions))
		Expect(restoreSessionOptions).To(ContainSubstring("IF @options & 16384 = 0 SET XACT_ABORT OFF;\n"))
		Expect(restoreSessionOptions).To(ContainSubstring("IF @options & 8192 <> 0 SET NUMERIC_ROUNDABORT ON;\n"))
		Expect(regexp.MustCompile(`(?mi)^\s*GO\s*$`).MatchString(script)).To(BeFalse())
		Expect(script).To(ContainSubstring("IF OBJECT_ID(N'[dbo].[orders]', N'U') IS NULL SET @c1 = 1;\nELSE IF NOT (EXISTS (SELECT 1 FROM sys.columns AS c WHERE c.object_id = OBJECT_ID(N'[dbo].[orders]') AND c.name = N'id' AND "))
		Expect(script).To(ContainSubstring("(SELECT COUNT(*) FROM sys.columns WHERE object_id = OBJECT_ID(N'[dbo].[orders]')) = 1"))
		Expect(script).To(ContainSubstring("IF @c1 = 1 EXEC sys.sp_executesql N'CREATE TABLE [dbo].[orders] ([id] int NOT NULL)';\n"))
		Expect(renderAcknowledged(artifact)).To(Equal(script))
	})

	It("adds a member only when absent, skips it in its exact desired shape and throws on any other shape", func() {
		current := scriptTable(label)
		desired := scriptTable(label, migrate.Column{Name: "total", Type: "int", Nullable: true, Default: &migrate.Default{Name: "DF_orders_total", Expression: "((0))"}})
		desired.Indexes = []migrate.Index{{Name: "IX_orders_label", Columns: []migrate.IndexColumn{{Name: "label"}}, Filter: "([label] IS NOT NULL)"}}
		desired.Checks = []migrate.Check{{Name: "CK_orders_total", Expression: "([total]>=(0))"}}
		script := renderAcknowledged(scriptArtifact(tables(current), tables(desired)))
		for _, guard := range []string{
			"COL_LENGTH(N'[dbo].[orders]', N'total') IS NULL SET @c1 = 1;\nELSE IF NOT EXISTS (SELECT 1 FROM sys.columns AS c WHERE c.object_id = OBJECT_ID(N'[dbo].[orders]') AND c.name = N'total' AND " + columnTypeSQL + " = N'int' AND c.is_nullable = 1 AND c.is_rowguidcol = 0 AND c.is_identity = 0 AND OBJECT_DEFINITION(c.default_object_id) = N'((0))') THROW",
			"NOT EXISTS (SELECT 1 FROM sys.indexes WHERE object_id = OBJECT_ID(N'[dbo].[orders]') AND name = N'IX_orders_label') SET @c3 = 1;\nELSE IF NOT EXISTS (SELECT 1 FROM sys.indexes AS i WHERE i.object_id = OBJECT_ID(N'[dbo].[orders]') AND i.name = N'IX_orders_label' AND ",
			"NOT EXISTS (SELECT 1 FROM sys.check_constraints WHERE parent_object_id = OBJECT_ID(N'[dbo].[orders]') AND name = N'CK_orders_total') SET @c2 = 1;\nELSE IF NOT EXISTS (SELECT 1 FROM sys.check_constraints AS k WHERE k.parent_object_id = OBJECT_ID(N'[dbo].[orders]') AND k.name = N'CK_orders_total' AND k.definition = N'([total]>=(0))') THROW",
		} {
			Expect(script).To(ContainSubstring("IF " + guard))
		}
		Expect(script).To(ContainSubstring("i.filter_definition = N'([label] IS NOT NULL)') THROW"))
		dropped := renderAcknowledged(scriptArtifact(tables(desired), tables(scriptTable())))
		Expect(dropped).To(ContainSubstring("IF COL_LENGTH(N'[dbo].[orders]', N'label') IS NOT NULL SET"))
		Expect(dropped).To(ContainSubstring("IF EXISTS (SELECT 1 FROM sys.indexes WHERE object_id = OBJECT_ID(N'[dbo].[orders]') AND name = N'IX_orders_label') SET"))
		Expect(renderAcknowledged(scriptArtifact(tables(desired), migrate.Schema{}))).To(ContainSubstring("IF OBJECT_ID(N'[dbo].[orders]', N'U') IS NOT NULL SET"))
	})

	It("guards a foreign key on its parent table", func() {
		items := migrate.Table{Name: migrate.Name{Schema: "dbo", Name: "items"}, Columns: []migrate.Column{{Name: "id", Type: "int"}}}
		withKey := items
		withKey.ForeignKeys = []migrate.ForeignKey{{Name: "FK_items_orders", Columns: []string{"id"}, References: scriptOrders, ReferencedColumns: []string{"id"}}}
		script := renderAcknowledged(scriptArtifact(tables(scriptTable(), items), tables(scriptTable(), withKey)))
		Expect(script).To(ContainSubstring("IF NOT EXISTS (SELECT 1 FROM sys.foreign_keys WHERE parent_object_id = OBJECT_ID(N'[dbo].[items]') AND name = N'FK_items_orders') SET @c1 = 1;\n" +
			"ELSE IF NOT EXISTS (SELECT 1 FROM sys.foreign_keys AS f WHERE f.parent_object_id = OBJECT_ID(N'[dbo].[items]') AND f.name = N'FK_items_orders' AND f.referenced_object_id = OBJECT_ID(N'[dbo].[orders]')"))
	})

	It("alters a column only from its exact baseline, skips its exact desired state and throws on anything else", func() {
		wider := label
		wider.Type = "nvarchar(100)"
		wider.Default = &migrate.Default{Name: "DF_orders_label", Expression: "(N'open')"}
		script := renderAcknowledged(scriptArtifact(tables(scriptTable(label)), tables(scriptTable(wider))))
		columnAt := "SELECT 1 FROM sys.columns AS c WHERE c.object_id = OBJECT_ID(N'[dbo].[orders]') AND c.name = N'label' AND " + columnTypeSQL
		Expect(script).To(ContainSubstring("IF EXISTS (" + columnAt + " = N'nvarchar(20)' AND c.is_nullable = 1 AND c.is_rowguidcol = 0 AND c.is_identity = 0 AND c.collation_name = N'Latin1_General_CI_AS' AND c.default_object_id = 0) SET @c1 = 1;\n"))
		Expect(script).To(ContainSubstring("ELSE IF NOT EXISTS (" + columnAt + " = N'nvarchar(100)' AND c.is_nullable = 1 AND c.is_rowguidcol = 0 AND c.is_identity = 0 AND c.collation_name = N'Latin1_General_CI_AS' AND OBJECT_DEFINITION(c.default_object_id) = N'(N''open'')') THROW 50000, N'"))
		Expect(script).To(ContainSubstring("matches neither its baseline nor its desired state', 1;\n"))
	})

	It("drops a column's default under the name the database gives it when the script runs", func() {
		defaulted := label
		defaulted.Default = &migrate.Default{Name: "DF_orders_label", Expression: "(N'pending')"}
		wider := defaulted
		wider.Type = "nvarchar(100)"
		for _, artifact := range []migrate.Artifact{
			scriptArtifact(tables(scriptTable(defaulted)), tables(scriptTable(wider))),
			scriptArtifact(tables(scriptTable(defaulted)), tables(scriptTable())),
		} {
			script := renderAcknowledged(artifact)
			Expect(script).NotTo(ContainSubstring("DROP CONSTRAINT [DF_orders_label]"))
			Expect(script).To(ContainSubstring("IF @c1 = 1\nBEGIN\nSET @df = NULL;\n" +
				"SELECT @df = dc.name FROM sys.default_constraints AS dc JOIN sys.columns AS c ON c.object_id = dc.parent_object_id AND c.column_id = dc.parent_column_id WHERE c.object_id = OBJECT_ID(N'[dbo].[orders]') AND c.name = N'label';\n" +
				"IF @df IS NOT NULL\nBEGIN\nSET @sql = N'ALTER TABLE [dbo].[orders] DROP CONSTRAINT ' + QUOTENAME(@df);\nEXEC sys.sp_executesql @sql;\nEND\nEND\n"))
		}
	})

	It("refuses to drop or modify a constraint SQL Server named itself, naming the change", func() {
		items := migrate.Table{Name: migrate.Name{Schema: "dbo", Name: "items"}, Columns: []migrate.Column{{Name: "id", Type: "int"}}}
		withKey := items
		withKey.ForeignKeys = []migrate.ForeignKey{{Name: "FK__items__id__5EBF139D", Columns: []string{"id"}, References: scriptOrders, ReferencedColumns: []string{"id"}}}
		artifact := scriptArtifact(tables(scriptTable(), withKey), tables(scriptTable(), items))
		_, err := RenderScript(artifact, ScriptOptions{Acknowledgements: Acknowledgements(artifact)})
		Expect(err).To(MatchError(And(ContainSubstring(artifact.Plan.Steps[0].Change.ID), ContainSubstring("system-named"))))
	})

	It("modifies a check from its exact baseline definition and skips only its exact desired definition", func() {
		from, to := scriptTable(), scriptTable()
		from.Checks = []migrate.Check{{Name: "CK_orders_id", Expression: "([id]>(0))"}}
		to.Checks = []migrate.Check{{Name: "CK_orders_id", Expression: "([id]>(1))"}}
		script := renderAcknowledged(scriptArtifact(tables(from), tables(to)))
		named := "SELECT 1 FROM sys.check_constraints AS k WHERE k.parent_object_id = OBJECT_ID(N'[dbo].[orders]') AND k.name = N'CK_orders_id' AND k.definition = "
		Expect(script).To(ContainSubstring("IF EXISTS (" + named + "N'([id]>(0))') SET @c1 = 1;\nELSE IF NOT EXISTS (" + named + "N'([id]>(1))') THROW"))
	})

	It("throws before any step when an index split around a column change is only partly applied", func() {
		indexed := migrate.Index{Name: "IX_label", Columns: []migrate.IndexColumn{{Name: "label"}}}
		from := scriptTable(label)
		from.Indexes = []migrate.Index{indexed}
		wider := label
		wider.Type = "nvarchar(100)"
		to := scriptTable(wider)
		indexed.Unique = true
		to.Indexes = []migrate.Index{indexed}
		artifact := scriptArtifact(tables(from), tables(to))
		Expect(artifact.Plan.Steps).To(HaveLen(3))
		script := renderAcknowledged(artifact)
		index, column := artifact.Plan.Steps[0].Change.ID, artifact.Plan.Steps[1].Change.ID
		Expect(script).To(ContainSubstring("IF NOT (@c1 = @c2) THROW 50000, " + literal("migration "+artifact.Hash+": changes "+index+", "+column+" depend on each other and only some of them are in their desired state") + ", 1;\nIF @c1 = 1 EXEC"))
	})

	It("runs CREATE OR ALTER modules unconditionally and guards module drops", func() {
		view := migrate.Definition{Name: migrate.Name{Schema: "dbo", Name: "order_ids"}, Kind: "VIEW", SQL: "CREATE VIEW dbo.order_ids AS SELECT N'x' AS id"}
		withView := migrate.Schema{Definitions: []migrate.Definition{view}}
		script := renderAcknowledged(scriptArtifact(migrate.Schema{}, withView))
		Expect(script).To(ContainSubstring("SET @c1 = 1;\nIF @c1 = 1 EXEC sys.sp_executesql N'CREATE OR ALTER VIEW dbo.order_ids AS SELECT N''x'' AS id';\n"))
		Expect(renderAcknowledged(scriptArtifact(withView, migrate.Schema{}))).To(ContainSubstring("IF OBJECT_ID(N'[dbo].[order_ids]', N'V') IS NOT NULL SET @c1 = 1;"))
	})

	It("escapes brackets in identifiers and quotes in literals", func() {
		odd := migrate.Table{Name: migrate.Name{Schema: "dbo", Name: "o]r'ders"}, Columns: []migrate.Column{{Name: "id", Type: "int"}}}
		script := renderAcknowledged(scriptArtifact(migrate.Schema{}, tables(odd)))
		Expect(script).To(ContainSubstring("IF OBJECT_ID(N'[dbo].[o]]r''ders]', N'U') IS NULL"))
		Expect(script).To(ContainSubstring("EXEC sys.sp_executesql N'CREATE TABLE [dbo].[o]]r''ders] ([id] int NOT NULL)';"))
	})

	It("requires each destructive change acknowledged exactly once", func() {
		artifact := scriptArtifact(tables(scriptTable(label)), tables(scriptTable()))
		id := Acknowledgements(artifact)[0]
		for _, acknowledgements := range [][]string{nil, {"unknown"}, {id, id}} {
			_, err := RenderScript(artifact, ScriptOptions{Acknowledgements: acknowledgements})
			Expect(err).To(MatchError(ContainSubstring("acknowledg")))
		}
		script, err := RenderScript(artifact, ScriptOptions{Acknowledgements: []string{id}})
		Expect(err).NotTo(HaveOccurred())
		Expect(script).To(ContainSubstring("-- Acknowledged destructive changes:\n--   " + id + "\n"))
	})

	It("refuses a tampered artifact", func() {
		artifact := scriptArtifact(migrate.Schema{}, tables(scriptTable()))
		artifact.Plan.Steps[0].SQL = "DROP TABLE [dbo].[orders]"
		_, err := RenderScript(artifact, ScriptOptions{})
		Expect(err).To(MatchError(ContainSubstring("hash mismatch")))
	})

	It("refuses a change it cannot guard, naming the change", func() {
		change := migrate.Change{ID: `["modify","attribute","dbo","orders","comment"]`, Action: "modify", Kind: "attribute", Object: scriptOrders, Member: "comment"}
		_, err := changeGuard(migrate.Request{}, change)
		Expect(err).To(MatchError(ContainSubstring(change.ID)))
	})
})
