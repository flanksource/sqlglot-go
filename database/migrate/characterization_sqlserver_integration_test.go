//go:build integration

package migrate

import (
	"context"
	"database/sql"
	"strings"
	"testing/fstest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver/sqlservertest"
)

// Characterization of the SQL Server deploy path (Apply, PendingSQLServer) at
// compatibility level 100: the statements it plans and what it leaves alone.
// One disposable server serves the ordered specs; each spec starts from the
// state the previous one left.

const characterizedOrdersHCL = `schema "dbo" {}

table "acme_orders" {
  schema = schema.dbo
  column "id" {
    type = int
    identity {
      seed      = 1
      increment = 1
    }
  }
  column "status" {
    type    = nvarchar(20)
    default = "open"
  }
  column "qty" {
    type    = QTY_TYPE
    default = 0
  }
  EXTRA_COLUMNS
  primary_key {
    columns = [column.id]
  }
}
`

const characterizedNoteColumn = `column "note" {
    type = nvarchar(50)
    null = true
  }`

const characterizedCountRoutine = `CREATE OR ALTER FUNCTION dbo.acme_order_count(@status NVARCHAR(20))
RETURNS INT
AS
BEGIN
    RETURN (SELECT COUNT(*) FROM dbo.acme_orders WHERE status = @status);
END
`

var characterizationOptions = []Option{WithDir("db"), WithRoutines("db/routines"), WithName("acme/characterization")}

func characterizationBundle(extraColumns, qtyType, routine string) fstest.MapFS {
	hcl := strings.NewReplacer("EXTRA_COLUMNS", extraColumns, "QTY_TYPE", qtyType).Replace(characterizedOrdersHCL)
	return fstest.MapFS{
		"db/schema.hcl":                    file(hcl),
		"db/routines/acme_order_count.sql": file(routine),
	}
}

var _ = Describe("SQL Server deploy path characterization", Ordered, Label("integration"), func() {
	var db *sql.DB
	apply := func(ctx context.Context, bundle fstest.MapFS) {
		Expect(Apply(ctx, "", bundle, append([]Option{WithDB(db)}, characterizationOptions...)...)).To(Succeed())
	}
	pending := func(ctx context.Context, bundle fstest.MapFS) SQLServerPending {
		result, err := PendingSQLServer(ctx, db, bundle, append([]Option{WithDB(db)}, characterizationOptions...)...)
		Expect(err).NotTo(HaveOccurred())
		return result
	}
	exec := func(ctx context.Context, statement string) {
		_, err := db.ExecContext(ctx, statement)
		Expect(err).NotTo(HaveOccurred())
	}
	count := func(ctx context.Context, query string) int {
		var n int
		Expect(db.QueryRowContext(ctx, query).Scan(&n)).To(Succeed())
		return n
	}

	BeforeAll(func(ctx SpecContext) {
		db = sqlservertest.OpenDisposable(ctx)
		exec(ctx, "ALTER DATABASE CURRENT SET COMPATIBILITY_LEVEL = 100")
		Expect(count(ctx, "SELECT compatibility_level FROM sys.databases WHERE name = DB_NAME()")).To(Equal(100))
	})

	It("creates the declared table and routine, and a re-apply plans nothing", func(ctx SpecContext) {
		bundle := characterizationBundle("", "int", characterizedCountRoutine)
		Expect(pending(ctx, bundle).Statements).To(Equal([]string{
			"CREATE TABLE [dbo].[acme_orders] ([id] int IDENTITY(1,1) NOT NULL, [status] nvarchar(20) NOT NULL CONSTRAINT [DF_acme_orders_status] DEFAULT N'open', [qty] int NOT NULL CONSTRAINT [DF_acme_orders_qty] DEFAULT 0, CONSTRAINT [PK_acme_orders] PRIMARY KEY CLUSTERED ([id]))",
			strings.TrimSuffix(characterizedCountRoutine, "\n"),
		}))
		apply(ctx, bundle)
		again := pending(ctx, bundle)
		Expect(again.Statements).To(BeEmpty())
		Expect(again.Changed()).To(BeFalse())
	})

	It("adds a declared column", func(ctx SpecContext) {
		bundle := characterizationBundle(characterizedNoteColumn, "int", characterizedCountRoutine)
		Expect(pending(ctx, bundle).Statements).To(Equal([]string{"ALTER TABLE [dbo].[acme_orders] ADD [note] nvarchar(50) NULL"}))
		apply(ctx, bundle)
		Expect(count(ctx, "SELECT COUNT(*) FROM sys.columns WHERE object_id = OBJECT_ID('dbo.acme_orders') AND name = 'note'")).To(Equal(1))
		Expect(pending(ctx, bundle).Statements).To(BeEmpty())
	})

	It("retypes an unindexed column, dropping its default before the ALTER and rebinding it after, keeping the row", func(ctx SpecContext) {
		exec(ctx, "INSERT INTO dbo.acme_orders (status, qty) VALUES ('open', 7)")
		bundle := characterizationBundle(characterizedNoteColumn, "bigint", characterizedCountRoutine)
		Expect(pending(ctx, bundle).Statements).To(Equal([]string{
			"ALTER TABLE [dbo].[acme_orders] DROP CONSTRAINT [DF_acme_orders_qty]",
			"ALTER TABLE [dbo].[acme_orders] ALTER COLUMN [qty] bigint NOT NULL",
			"ALTER TABLE [dbo].[acme_orders] ADD CONSTRAINT [DF_acme_orders_qty] DEFAULT 0 FOR [qty]",
		}))
		apply(ctx, bundle)
		Expect(count(ctx, "SELECT CAST(qty AS int) FROM dbo.acme_orders WHERE status = 'open'")).To(Equal(7))
		Expect(count(ctx, "SELECT COUNT(*) FROM sys.columns c JOIN sys.types t ON t.user_type_id = c.user_type_id WHERE c.object_id = OBJECT_ID('dbo.acme_orders') AND c.name = 'qty' AND t.name = 'bigint'")).To(Equal(1))
		Expect(pending(ctx, bundle).Statements).To(BeEmpty())
	})

	It("alters only the routine whose body changed", func(ctx SpecContext) {
		edited := strings.Replace(characterizedCountRoutine, "COUNT(*)", "COUNT(*) + 0", 1)
		bundle := characterizationBundle(characterizedNoteColumn, "bigint", edited)
		Expect(pending(ctx, bundle).Statements).To(Equal([]string{strings.TrimSuffix(edited, "\n")}))
		apply(ctx, bundle)
		Expect(pending(ctx, bundle).Statements).To(BeEmpty())
	})

	It("drops an index the complete declaration does not declare, keeps a column it does not declare, and reports that column on every run", func(ctx SpecContext) {
		exec(ctx, "CREATE INDEX IX_dba_tuning ON dbo.acme_orders (status)")
		exec(ctx, "ALTER TABLE dbo.acme_orders ADD dba_note nvarchar(20) NULL")
		bundle := characterizationBundle(characterizedNoteColumn, "bigint", strings.Replace(characterizedCountRoutine, "COUNT(*)", "COUNT(*) + 0", 1))
		warning := `column "dba_note" of table dbo.acme_orders is in the database but not declared, and was not dropped: declare the column or mark the table ` + "`partial = true`" + `; SQL Server bundles do not support WithDrops`

		planned := pending(ctx, bundle)
		Expect(planned.Statements).To(Equal([]string{"DROP INDEX [IX_dba_tuning] ON [dbo].[acme_orders]"}))
		Expect(planned.Warnings).To(Equal([]string{warning}))
		Expect(planned.Changed()).To(BeTrue())

		apply(ctx, bundle)
		Expect(count(ctx, "SELECT COUNT(*) FROM sys.indexes WHERE object_id = OBJECT_ID('dbo.acme_orders') AND name = 'IX_dba_tuning'")).To(Equal(0))
		Expect(count(ctx, "SELECT COUNT(*) FROM sys.columns WHERE object_id = OBJECT_ID('dbo.acme_orders') AND name = 'dba_note'")).To(Equal(1))

		again := pending(ctx, bundle)
		Expect(again.Statements).To(BeEmpty())
		Expect(again.Warnings).To(Equal([]string{warning}))
	})

	It("keeps an index and a column a partial declaration does not name, with nothing reported", func(ctx SpecContext) {
		exec(ctx, "CREATE INDEX IX_dba_tuning ON dbo.acme_orders (status)")
		bundle := characterizationBundle(characterizedNoteColumn, "bigint", strings.Replace(characterizedCountRoutine, "COUNT(*)", "COUNT(*) + 0", 1))
		bundle["db/schema.hcl"] = file(strings.Replace(string(bundle["db/schema.hcl"].Data), "  schema = schema.dbo\n", "  schema = schema.dbo\n  partial = true\n", 1))

		planned := pending(ctx, bundle)
		Expect(planned.Statements).To(BeEmpty())
		Expect(planned.Warnings).To(BeEmpty())
		apply(ctx, bundle)
		Expect(count(ctx, "SELECT COUNT(*) FROM sys.indexes WHERE object_id = OBJECT_ID('dbo.acme_orders') AND name = 'IX_dba_tuning'")).To(Equal(1))
		Expect(count(ctx, "SELECT COUNT(*) FROM sys.columns WHERE object_id = OBJECT_ID('dbo.acme_orders') AND name = 'dba_note'")).To(Equal(1))
	})
})
