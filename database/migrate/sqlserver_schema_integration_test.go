//go:build integration

package migrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing/fstest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	backend "github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver/sqlservertest"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

// The bundle's schema step on a real SQL Server at compatibility level 100:
// dependent indexes and keys around a retype, the data guard, stale plans, the
// schema artifact as the plan file embeds it, and routines beside tables.

const retypeItemsHCL = `schema "dbo" {}

table "acme_items" {
  schema = schema.dbo
  column "ref" {
    type = REF_TYPE
  }
  column "label" {
    type = nvarchar(40)
    null = true
  }
  primary_key {
    columns = [column.ref]
  }
}
`

const itemLabelRoutine = `CREATE OR ALTER FUNCTION dbo.acme_item_label(@ref uniqueidentifier)
RETURNS NVARCHAR(40)
AS
BEGIN
    RETURN (SELECT label FROM dbo.acme_items WHERE ref = @ref);
END
`

const itemNoteRoutine = `CREATE OR ALTER FUNCTION dbo.acme_item_label(@ref uniqueidentifier)
RETURNS NVARCHAR(80)
AS
BEGIN
    RETURN (SELECT label + COALESCE(note, N'') FROM dbo.acme_items WHERE ref = @ref);
END
`

var schemaOptions = []Option{WithDir("db"), WithRoutines("db/routines"), WithName("acme/schema")}

func retypeBundle(refType string, extra ...string) fstest.MapFS {
	hcl := strings.NewReplacer("REF_TYPE", refType).Replace(retypeItemsHCL)
	if len(extra) > 0 {
		hcl = strings.Replace(hcl, "  primary_key {", extra[0]+"\n  primary_key {", 1)
	}
	return fstest.MapFS{"db/schema.hcl": file(hcl), "db/routines/acme_item_label.sql": file(itemLabelRoutine)}
}

var _ = Describe("SQL Server schema step", Ordered, Label("integration"), func() {
	var db *sql.DB
	exec := func(ctx context.Context, statements ...string) {
		GinkgoHelper()
		for _, statement := range statements {
			_, err := db.ExecContext(ctx, statement)
			Expect(err).NotTo(HaveOccurred(), statement)
		}
	}
	scalar := func(ctx context.Context, query string) string {
		GinkgoHelper()
		var value string
		Expect(db.QueryRowContext(ctx, query).Scan(&value)).To(Succeed(), query)
		return value
	}
	columnType := func(ctx context.Context, table, column string) string {
		return scalar(ctx, "SELECT t.name FROM sys.columns c JOIN sys.types t ON t.user_type_id = c.user_type_id WHERE c.object_id = OBJECT_ID('dbo."+table+"') AND c.name = '"+column+"'")
	}
	apply := func(ctx context.Context, bundle fstest.MapFS) error {
		return Apply(ctx, "", bundle, append([]Option{WithDB(db)}, schemaOptions...)...)
	}
	pending := func(ctx context.Context, bundle fstest.MapFS) SQLServerPending {
		GinkgoHelper()
		result, err := PendingSQLServer(ctx, db, bundle, append([]Option{WithDB(db)}, schemaOptions...)...)
		Expect(err).NotTo(HaveOccurred())
		return result
	}

	BeforeAll(func(ctx SpecContext) {
		db = sqlservertest.OpenDisposable(ctx)
		exec(ctx, "ALTER DATABASE CURRENT SET COMPATIBILITY_LEVEL = 100")
		Expect(scalar(ctx, "SELECT CAST(compatibility_level AS varchar(10)) FROM sys.databases WHERE name = DB_NAME()")).To(Equal("100"))
	})

	It("retypes a varchar(36) column to uniqueidentifier under a primary key and a DBA index a partial declaration leaves, keeping rows and both indexes", func(ctx SpecContext) {
		Expect(apply(ctx, retypeBundle("varchar(36)"))).To(Succeed())
		exec(ctx,
			"INSERT INTO dbo.acme_items (ref, label) VALUES ('0A1B2C3D-0000-4000-8000-00000000AAAA', N'first'), ('0A1B2C3D-0000-4000-8000-00000000BBBB', N'second')",
			"CREATE NONCLUSTERED INDEX IX_dba_ref ON dbo.acme_items (ref)")

		retyped := retypeBundle("uniqueidentifier")
		retyped["db/schema.hcl"] = file(strings.Replace(string(retyped["db/schema.hcl"].Data), "  schema = schema.dbo\n", "  schema = schema.dbo\n  partial = true\n", 1))
		changes := changeKinds(pendingSQLServerChanges(ctx, db, retyped))
		Expect(changes).To(ContainElements("modify column dbo.acme_items.ref"))
		Expect(changes).To(ContainElement(HavePrefix("rebuild ")))

		Expect(apply(ctx, retyped)).To(Succeed())
		Expect(columnType(ctx, "acme_items", "ref")).To(Equal("uniqueidentifier"))
		Expect(scalar(ctx, "SELECT CAST(COUNT(*) AS varchar(10)) FROM dbo.acme_items WHERE ref IN ('0A1B2C3D-0000-4000-8000-00000000AAAA', '0A1B2C3D-0000-4000-8000-00000000BBBB')")).To(Equal("2"))
		Expect(scalar(ctx, "SELECT CAST(COUNT(*) AS varchar(10)) FROM sys.indexes WHERE object_id = OBJECT_ID('dbo.acme_items') AND name = 'IX_dba_ref'")).To(Equal("1"))
		Expect(scalar(ctx, "SELECT CAST(COUNT(*) AS varchar(10)) FROM sys.indexes WHERE object_id = OBJECT_ID('dbo.acme_items') AND is_primary_key = 1")).To(Equal("1"))
		Expect(pending(ctx, retyped).Changed()).To(BeFalse())
	})

	It("fails a retype over a value that is not a GUID with the precondition's message, changing nothing, while the pre scripts that ran stay recorded", func(ctx SpecContext) {
		bundle := fstest.MapFS{
			"db/schema.hcl":                  file(strings.NewReplacer("REF_TYPE", "varchar(36)", "acme_items", "acme_bad").Replace(retypeItemsHCL)),
			"db/routines/acme_bad_label.sql": file(strings.NewReplacer("acme_items", "acme_bad", "acme_item_label", "acme_bad_label").Replace(itemLabelRoutine)),
		}
		Expect(apply(ctx, bundle)).To(Succeed())
		exec(ctx, "INSERT INTO dbo.acme_bad (ref, label) VALUES ('not-a-guid', N'broken')", "CREATE NONCLUSTERED INDEX IX_dba_bad ON dbo.acme_bad (ref)")

		bundle["db/schema.hcl"] = file(strings.NewReplacer("REF_TYPE", "uniqueidentifier", "acme_items", "acme_bad").Replace(retypeItemsHCL))
		bundle["db/pre/001_marker.sql"] = file("-- phase: pre\nCREATE TABLE dbo.pre_marker (id int NOT NULL);\n")
		err := apply(ctx, bundle)
		var failed *engine.PreconditionError
		Expect(errors.As(err, &failed)).To(BeTrue(), "%v", err)
		Expect(failed.Message).To(And(ContainSubstring("[acme_bad].ref"), HaveSuffix("holds values that are not canonical GUIDs, which uniqueidentifier would reject")))
		Expect(columnType(ctx, "acme_bad", "ref")).To(Equal("varchar"))
		Expect(scalar(ctx, "SELECT CAST(COUNT(*) AS varchar(10)) FROM sys.indexes WHERE object_id = OBJECT_ID('dbo.acme_bad') AND name = 'IX_dba_bad'")).To(Equal("1"))
		Expect(scalar(ctx, "SELECT CAST(COUNT(*) AS varchar(10)) FROM dbo.acme_bad")).To(Equal("1"))
		Expect(scalar(ctx, "SELECT CAST(COUNT(*) AS varchar(10)) FROM sys.tables WHERE name = 'pre_marker'")).To(Equal("1"), "a pre script runs and commits before the schema step")
		Expect(scalar(ctx, "SELECT CAST(COUNT(*) AS varchar(10)) FROM dbo.sqlglot_migrations WHERE path LIKE '%001_marker.sql'")).To(Equal("1"), "and stays recorded: nothing rolls it back")

		exec(ctx, "UPDATE dbo.acme_bad SET ref = '0A1B2C3D-0000-4000-8000-00000000CCCC'")
		Expect(apply(ctx, bundle)).To(Succeed())
		Expect(columnType(ctx, "acme_bad", "ref")).To(Equal("uniqueidentifier"))
	})

	It("plans nothing for a table whose primary key SQL Server named itself", func(ctx SpecContext) {
		exec(ctx, "CREATE TABLE dbo.acme_legacy (id int NOT NULL PRIMARY KEY, label nvarchar(40) NULL)")
		bundle := fstest.MapFS{"db/schema.hcl": file(`schema "dbo" {}

table "acme_legacy" {
  schema = schema.dbo
  column "id" {
    type = int
  }
  column "label" {
    type = nvarchar(40)
    null = true
  }
  primary_key {
    columns = [column.id]
  }
}
`)}
		result, err := PendingSQLServer(ctx, db, bundle, WithDB(db), WithDir("db"), WithName("acme/legacy"))
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Statements).To(BeEmpty())
		Expect(result.Changed()).To(BeFalse())
	})

	It("resumes an interrupted schema step from the plan file, skipping the units that committed", func(ctx SpecContext) {
		tables := func(refType string) fstest.MapFS {
			hcl := `schema "dbo" {}
`
			for _, name := range []string{"res_a", "res_b"} {
				hcl += strings.NewReplacer("acme_items", name, "REF_TYPE", refType).Replace(retypeItemsHCL[strings.Index(retypeItemsHCL, "table"):])
			}
			return fstest.MapFS{"db/schema.hcl": file(hcl)}
		}
		options := []Option{WithDB(db), WithDir("db"), WithName("acme/resume")}
		Expect(Apply(ctx, "", tables("varchar(36)"), options...)).To(Succeed())
		exec(ctx,
			"INSERT INTO dbo.res_a (ref) VALUES ('0A1B2C3D-0000-4000-8000-00000000AAAA')",
			"INSERT INTO dbo.res_b (ref) VALUES ('not-a-guid')")

		planned, err := PendingSQLServer(ctx, db, tables("uniqueidentifier"), options...)
		Expect(err).NotTo(HaveOccurred())
		Expect(planned.Plan.Bundle.Schema.Units).To(HaveLen(2))

		result, err := backend.Apply(ctx, db, *planned.Plan)
		var failed *engine.PreconditionError
		Expect(errors.As(err, &failed)).To(BeTrue(), "%v", err)
		Expect(result.Schema).To(Equal("partial"))
		Expect(columnType(ctx, "res_a", "ref")).To(Equal("uniqueidentifier"))
		Expect(columnType(ctx, "res_b", "ref")).To(Equal("varchar"))

		exec(ctx, "UPDATE dbo.res_b SET ref = '0A1B2C3D-0000-4000-8000-00000000BBBB'")
		result, err = backend.Apply(ctx, db, *planned.Plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Schema).To(Equal("applied"))
		Expect(columnType(ctx, "res_b", "ref")).To(Equal("uniqueidentifier"))
	})

	It("reports a plan file stale when the declared table changed after it was planned", func(ctx SpecContext) {
		bundle := retypeBundle("uniqueidentifier", `column "stamp" {
    type = int
    null = true
  }`)
		planned := pending(ctx, bundle)
		Expect(planned.Changed()).To(BeTrue())
		exec(ctx, "ALTER TABLE dbo.acme_items ADD dba_extra int NULL")

		result, err := backend.Apply(ctx, db, *planned.Plan)
		var stale *backend.StalePlanError
		Expect(errors.As(err, &stale)).To(BeTrue(), "%v", err)
		Expect(result.Outcome).To(Equal("stale"))
		Expect(scalar(ctx, "SELECT CAST(COUNT(*) AS varchar(10)) FROM sys.columns WHERE object_id = OBJECT_ID('dbo.acme_items') AND name = 'stamp'")).To(Equal("0"))
		exec(ctx, "ALTER TABLE dbo.acme_items DROP COLUMN dba_extra")
	})

	It("reports a plan file stale when the database capabilities changed after it was planned", func(ctx SpecContext) {
		bundle := retypeBundle("uniqueidentifier", `column "stamp" {
    type = int
    null = true
  }`)
		planned := pending(ctx, bundle)
		exec(ctx, "ALTER DATABASE CURRENT SET COMPATIBILITY_LEVEL = 130")
		DeferCleanup(func(ctx SpecContext) { exec(ctx, "ALTER DATABASE CURRENT SET COMPATIBILITY_LEVEL = 100") })

		_, err := backend.Apply(ctx, db, *planned.Plan)
		var stale *backend.StalePlanError
		Expect(errors.As(err, &stale)).To(BeTrue(), "%v", err)
	})

	It("returns from PlanSchema the artifact the plan file embeds, and a plan file of it applies after a JSON round trip", func(ctx SpecContext) {
		bundle := retypeBundle("uniqueidentifier", `column "stamp" {
    type = int
    null = true
  }`)
		artifact, err := PlanSchema(ctx, db, bundle, schemaOptions...)
		Expect(err).NotTo(HaveOccurred())
		planned := pending(ctx, bundle)
		Expect(planned.Plan.Bundle.Schema).NotTo(BeNil())
		Expect(*planned.Plan.Bundle.Schema).To(Equal(artifact))
		Expect(planned.Plan.Version).To(Equal(backend.PlanFileVersion))
		Expect(planned.Statements).To(Equal([]string{
			"DROP INDEX [IX_dba_ref] ON [dbo].[acme_items]",
			"ALTER TABLE [dbo].[acme_items] ADD [stamp] int NULL",
		}), "the bundle declares acme_items completely, so the DBA index an earlier spec left is dropped")
		Expect(planned.Plan.Pretty().String()).To(ContainSubstring("add column dbo.acme_items.stamp"))

		encoded, err := json.Marshal(planned.Plan)
		Expect(err).NotTo(HaveOccurred())
		var decoded backend.PlanFile
		Expect(json.Unmarshal(encoded, &decoded)).To(Succeed())
		Expect(decoded.Verify()).To(Succeed())
		result, err := backend.Apply(ctx, db, decoded)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Schema).To(Equal("applied"))
		Expect(pending(ctx, bundle).Changed()).To(BeFalse())
	})

	It("applies a routine edit and a table change of one bundle in order, the routine reading the new column", func(ctx SpecContext) {
		bundle := retypeBundle("uniqueidentifier", `column "stamp" {
    type = int
    null = true
  }
  column "note" {
    type = nvarchar(40)
    null = true
  }`)
		bundle["db/routines/acme_item_label.sql"] = file(itemNoteRoutine)
		changes := changeKinds(pendingSQLServerChanges(ctx, db, bundle))
		Expect(changes).To(ConsistOf("add column dbo.acme_items.note", "modify function dbo.acme_item_label"))

		Expect(apply(ctx, bundle)).To(Succeed())
		Expect(scalar(ctx, "SELECT OBJECT_DEFINITION(OBJECT_ID('dbo.acme_item_label'))")).To(ContainSubstring("COALESCE(note"))
		Expect(scalar(ctx, "SELECT CAST(COUNT(*) AS varchar(10)) FROM sys.columns WHERE object_id = OBJECT_ID('dbo.acme_items') AND name = 'note'")).To(Equal("1"))
		Expect(pending(ctx, bundle).Changed()).To(BeFalse())
	})
})
