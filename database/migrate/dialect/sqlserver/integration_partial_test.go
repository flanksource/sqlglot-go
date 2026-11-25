//go:build integration

package sqlserver

import (
	"slices"

	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("complete and partial declarations against the live catalog", Label("integration"), func() {
	It("drops an undeclared column with its default constraint, an index and a check, keeping the other rows' data", func(ctx SpecContext) {
		mustExec(ctx, db,
			"CREATE SCHEMA completedrop",
			"CREATE TABLE completedrop.items (id int NOT NULL PRIMARY KEY, label nvarchar(20) NOT NULL, gone int NOT NULL CONSTRAINT DF_items_gone DEFAULT 5)",
			"CREATE INDEX IX_items_label ON completedrop.items (label)",
			"ALTER TABLE completedrop.items ADD CONSTRAINT CK_items_label CHECK (label <> N'')",
			"INSERT INTO completedrop.items (id, label) VALUES (1, N'kept'), (2, N'also kept')")
		scope := declared("completedrop", "items")
		desired := cloneSchema(inspect(ctx, db, scope).Schema)
		table := tableOf(desired, migrate.Name{Schema: "completedrop", Name: "items"})
		table.Columns = table.Columns[:2]
		table.Indexes = slices.DeleteFunc(table.Indexes, func(i migrate.Index) bool { return !i.Primary })
		Expect(table.Indexes).To(HaveLen(1))
		table.Checks = nil

		artifact, err := planLive(ctx, db, scope, desired)
		Expect(err).NotTo(HaveOccurred())
		Expect(artifact.Preserved).To(BeEmpty())
		Expect(changeIDs(artifact, "drop")).To(ConsistOf(
			`["drop","column","completedrop","items","gone"]`,
			`["drop","index","completedrop","items","IX_items_label"]`,
			`["drop","check","completedrop","items","CK_items_label"]`))
		Expect(engine.Acknowledgements(artifact)).To(HaveLen(3))

		result, err := applyArtifact(ctx, db, artifact)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Outcome).To(Equal(engine.Applied))
		Expect(scalar[int](ctx, db, "SELECT COUNT(*) FROM sys.columns WHERE object_id = OBJECT_ID('completedrop.items') AND name = 'gone'")).To(Equal(0))
		Expect(scalar[int](ctx, db, "SELECT COUNT(*) FROM sys.default_constraints WHERE parent_object_id = OBJECT_ID('completedrop.items')")).To(Equal(0))
		Expect(scalar[string](ctx, db, "SELECT STRING_AGG(label, ',') FROM completedrop.items")).To(ContainSubstring("kept"))
		Expect(scalar[int](ctx, db, "SELECT COUNT(*) FROM completedrop.items")).To(Equal(2))

		again, err := planLive(ctx, db, scope, desired)
		Expect(err).NotTo(HaveOccurred())
		Expect(again.Changes).To(BeEmpty())
	})

	It("retypes one column of a partial declaration of a wide table and leaves everything else as the catalog had it", func(ctx SpecContext) {
		mustExec(ctx, db,
			"CREATE SCHEMA widepartial",
			`CREATE TABLE widepartial.wide (id int NOT NULL PRIMARY KEY, c1 int NULL, c2 nvarchar(30) NOT NULL CONSTRAINT DF_wide_c2 DEFAULT N'x',
				c3 int NOT NULL, c4 decimal(10,2) NULL, c5 datetime2 NULL, c6 nvarchar(max) NULL)`,
			"CREATE INDEX IX_wide_c2 ON widepartial.wide (c2) INCLUDE (c4)",
			"ALTER TABLE widepartial.wide ADD CONSTRAINT CK_wide_c3 CHECK (c3 >= 0)",
			"INSERT INTO widepartial.wide (id, c1, c2, c3) VALUES (1, 7, N'a', 1)")
		name := migrate.Name{Schema: "widepartial", Name: "wide"}
		scope := declared("widepartial", "wide")
		before := cloneSchema(inspect(ctx, db, scope).Schema)

		desired := cloneSchema(before)
		table := tableOf(desired, name)
		table.Partial = true
		table.Columns = []migrate.Column{*column(table, "id"), *column(table, "c1")}
		table.Indexes, table.Checks, table.ForeignKeys = nil, nil, nil
		column(table, "c1").Type = "bigint"

		artifact, err := planLive(ctx, db, scope, desired)
		Expect(err).NotTo(HaveOccurred())
		Expect(artifact.Changes).To(ConsistOf(HaveField("ID", `["modify","column","widepartial","wide","c1"]`)))
		Expect(artifact.Preserved).NotTo(BeEmpty())
		Expect(engine.Acknowledgements(artifact)).NotTo(ContainElement(ContainSubstring(`"drop"`)))

		result, err := applyArtifact(ctx, db, artifact)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Outcome).To(Equal(engine.Applied))

		expected := cloneSchema(before)
		column(tableOf(expected, name), "c1").Type = "bigint"
		Expect(inspect(ctx, db, scope).Schema).To(Equal(expected), "only c1 changed; every other column, index, default and check is as before")
		Expect(scalar[int](ctx, db, "SELECT c1 FROM widepartial.wide WHERE id = 1")).To(Equal(7))

		again, err := planLive(ctx, db, scope, desired)
		Expect(err).NotTo(HaveOccurred())
		Expect(again.Changes).To(BeEmpty())
	})

	It("fails a partial declaration of a table the database does not have", func(ctx SpecContext) {
		mustExec(ctx, db, "CREATE SCHEMA partialmissing")
		desired := cloneSchema(migrate.Schema{Tables: []migrate.Table{{
			Name: migrate.Name{Schema: "partialmissing", Name: "absent"}, Partial: true,
			Columns: []migrate.Column{{Name: "id", Type: "int"}},
		}}})
		_, err := planLive(ctx, db, declared("partialmissing", "absent"), desired)
		Expect(err).To(MatchError(ContainSubstring("table partialmissing.absent is declared partial and does not exist: a partial declaration cannot create a table")))
	})
})
