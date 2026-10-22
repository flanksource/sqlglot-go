package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing/fstest"
	"time"

	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlclient"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/sqlglot-go/database/inspect/inspector"
	executor "github.com/flanksource/sqlglot-go/database/migrate/sqlserver"
)

// SQL Server integration uses an explicit disposable admin URL. The integration
// harness copies tests into a container, so local and remote Docker work alike.

const integrationTablesHCL = `schema "dbo" {}

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
  column "total" {
    type    = decimal(18, 2)
    default = 0
  }
  column "created_at" {
    type    = datetime2(3)
    default = sql("sysutcdatetime()")
  }
  primary_key {
    columns = [column.id]
  }
  index "IX_acme_orders_status" {
    columns = [column.status]
    include = [column.total]
    where   = "status <> 'closed'"
  }
  check "CK_acme_orders_total" {
    expr = "total >= 0"
  }
}

table "acme_order_lines" {
  schema = schema.dbo
  column "line_id" {
    type = uniqueidentifier
  }
  column "order_id" {
    type = int
  }
  column "sku" {
    type = sql("varchar(max)")
    null = true
  }
  primary_key {
    columns  = [column.line_id]
    clustered = false
  }
  foreign_key "FK_acme_order_lines_order" {
    columns     = [column.order_id]
    ref_columns = [table.acme_orders.column.id]
    on_delete   = CASCADE
  }
}
`

const integrationCountOrders = `-- Counts a customer's orders.
CREATE PROCEDURE dbo.acme_count_orders
    @status NVARCHAR(20),
    @count INT OUTPUT
AS
BEGIN
    SET NOCOUNT ON;
    IF NOT EXISTS (SELECT 1 FROM dbo.acme_orders WHERE status = @status)
        RETURN;
    SET @count = (SELECT COUNT(*) FROM dbo.acme_orders WHERE status = @status);
END
GO
`

const integrationOrderTotal = `CREATE OR ALTER FUNCTION dbo.acme_order_total(@order_id INT)
RETURNS DECIMAL(18, 2)
AS
BEGIN
    RETURN (SELECT total FROM dbo.acme_orders WHERE id = @order_id);
END
`

func integrationBundle() fstest.MapFS {
	return fstest.MapFS{
		"db/schema.hcl":                     file(integrationTablesHCL),
		"db/routines/acme_count_orders.sql": file(integrationCountOrders),
		"db/routines/acme_order_total.sql":  file(integrationOrderTotal),
	}
}

var integrationOptions = []Option{WithDir("db"), WithRoutines("db/routines"), WithName("acme/integration")}

var _ = Describe("SQL Server migrations", Ordered, Label("integration"), func() {
	var adminURL string

	BeforeAll(func(ctx SpecContext) {
		adminURL = sqlServerAdminURL(ctx)
	})

	DescribeTable("reconcile tables and routines idempotently",
		func(ctx SpecContext, compatibilityLevel int, via connectionForm) {
			dsn, db := sqlServerDatabase(ctx, adminURL, compatibilityLevel)
			reconcileScenario(ctx, sqlServerApplier(dsn, db, via), dsn, db)
		},
		Entry("at the engine's default compatibility level", 0, viaURL),
		Entry("at compatibility level 100", 100, viaURL),
		Entry("through an ADO connection string", 0, viaADO),
		Entry("through WithDB on the caller's handle, which stays open", 0, viaHandle),
	)

	It("reads the migrated database through the catalog inspector", func(ctx SpecContext) {
		dsn, db := sqlServerDatabase(ctx, adminURL, 0)
		Expect(Apply(ctx, dsn, integrationBundle(), integrationOptions...)).To(Succeed())
		inspectCatalog(ctx, db)
	})

	It("previews an HCL and routine bundle without mutation and applies its native artifact", func(ctx SpecContext) {
		_, db := sqlServerDatabase(ctx, adminURL, 100)
		preview, err := PreviewBundle(ctx, db, "sqlserver", integrationBundle(), integrationOptions...)
		Expect(err).NotTo(HaveOccurred())
		Expect(preview.SQLServerArtifact).NotTo(BeNil())
		var count int
		Expect(db.QueryRowContext(ctx, "SELECT count(*) FROM sys.tables WHERE is_ms_shipped = 0").Scan(&count)).To(Succeed())
		Expect(count).To(BeZero())
		artifact := *preview.SQLServerArtifact
		result, err := executor.Apply(ctx, db, executor.Request{Artifact: artifact, Hash: artifact.Hash})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Outcome).To(Equal(executor.Applied))
		result, err = executor.Apply(ctx, db, executor.Request{Artifact: artifact, Hash: artifact.Hash})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Outcome).To(Equal(executor.AlreadyApplied))
		again, err := PreviewBundle(ctx, db, "sqlserver", integrationBundle(), integrationOptions...)
		Expect(err).NotTo(HaveOccurred())
		Expect(again.SQLServerArtifact.Plan.Steps).To(BeEmpty())
	})
})

// connectionForm is how a spec hands the database to Apply.
type connectionForm string

const (
	viaURL    connectionForm = "sqlserver:// URL"
	viaADO    connectionForm = "ADO connection string"
	viaHandle connectionForm = "WithDB handle"
)

// sqlServerApplier applies a bundle to the database at dsn, whose open
// handle is db, in the given connection form.
func sqlServerApplier(dsn string, db *sql.DB, via connectionForm) func(context.Context, fstest.MapFS) error {
	return func(ctx context.Context, bundle fstest.MapFS) error {
		switch via {
		case viaADO:
			return Apply(ctx, adoConnectionString(dsn), bundle, integrationOptions...)
		case viaHandle:
			return Apply(ctx, "", bundle, append([]Option{WithDB(db)}, integrationOptions...)...)
		default:
			return Apply(ctx, dsn, bundle, integrationOptions...)
		}
	}
}

// adoConnectionString rewrites a sqlserver:// URL as the equivalent ADO
// key=value connection string.
func adoConnectionString(dsn string) string {
	u, err := url.Parse(dsn)
	Expect(err).ToNot(HaveOccurred())
	password, _ := u.User.Password()
	pairs := []string{"server=" + u.Hostname(), "user id=" + u.User.Username(), "password=" + password,
		"database=" + u.Query().Get("database")}
	if port := u.Port(); port != "" {
		pairs = append(pairs, "port="+port)
	}
	return strings.Join(pairs, ";")
}

func reconcileScenario(ctx context.Context, apply func(context.Context, fstest.MapFS) error, dsn string, db *sql.DB) {
	bundle := integrationBundle()
	By("applying a table, an index, a foreign key and two routines")
	Expect(apply(ctx, bundle)).To(Succeed())
	Expect(pendingSQLServerChanges(ctx, dsn, bundle)).To(BeEmpty())
	_, err := db.ExecContext(ctx, "CREATE INDEX IX_dba_tuning ON dbo.acme_orders (created_at)")
	Expect(err).ToNot(HaveOccurred())
	before := modifyDates(ctx, db)
	Expect(before).To(HaveLen(4))

	By("re-applying the same bundle, which changes nothing")
	Expect(pendingSQLServerChanges(ctx, dsn, bundle)).To(BeEmpty())
	Expect(apply(ctx, bundle)).To(Succeed())
	Expect(modifyDates(ctx, db)).To(Equal(before))
	Expect(objectExists(ctx, db, "IX_dba_tuning")).To(BeTrue(), "an index the bundle does not declare is kept")

	By("editing one routine's body, which alters exactly that routine")
	bundle["db/routines/acme_order_total.sql"] = file(strings.Replace(integrationOrderTotal, "RETURN (SELECT total", "RETURN (SELECT total * 2", 1))
	Expect(changeKinds(pendingSQLServerChanges(ctx, dsn, bundle))).To(Equal([]string{"*schema.ModifyFunc"}))
	Expect(apply(ctx, bundle)).To(Succeed())
	after := modifyDates(ctx, db)
	Expect(after["acme_order_total"]).To(BeTemporally(">", before["acme_order_total"]))
	delete(after, "acme_order_total")
	delete(before, "acme_order_total")
	Expect(after).To(Equal(before))
	Expect(pendingSQLServerChanges(ctx, dsn, bundle)).To(BeEmpty())

	By("removing a routine from the bundle, which leaves it in place")
	delete(bundle, "db/routines/acme_count_orders.sql")
	Expect(apply(ctx, bundle)).To(Succeed())
	Expect(objectExists(ctx, db, "acme_count_orders")).To(BeTrue())
}

func pendingSQLServerChanges(ctx context.Context, dsn string, bundle fstest.MapFS) []schema.Change {
	desired, err := sqlServerDesired(bundle, resolveOptions(integrationOptions))
	Expect(err).ToNot(HaveOccurred())
	client, err := sqlclient.Open(ctx, dsn)
	Expect(err).ToNot(HaveOccurred())
	defer client.Close() //nolint:errcheck
	changes, err := sqlServerChanges(ctx, client, desired, nil)
	Expect(err).ToNot(HaveOccurred())
	return changes
}

func changeKinds(changes []schema.Change) []string {
	kinds := make([]string, len(changes))
	for i, change := range changes {
		kinds[i] = fmt.Sprintf("%T", change)
	}
	return kinds
}

func modifyDates(ctx context.Context, db *sql.DB) map[string]time.Time {
	rows, err := db.QueryContext(ctx, `SELECT name, modify_date FROM sys.objects
		WHERE name IN ('acme_orders', 'acme_order_lines', 'acme_count_orders', 'acme_order_total')`)
	Expect(err).ToNot(HaveOccurred())
	defer rows.Close() //nolint:errcheck
	dates := map[string]time.Time{}
	for rows.Next() {
		var name string
		var modified time.Time
		Expect(rows.Scan(&name, &modified)).To(Succeed())
		dates[name] = modified
	}
	Expect(rows.Err()).ToNot(HaveOccurred())
	return dates
}

func objectExists(ctx context.Context, db *sql.DB, name string) bool {
	var count int
	Expect(db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM sys.objects WHERE name = @p1)
		+ (SELECT COUNT(*) FROM sys.indexes WHERE name = @p1)`, name).Scan(&count)).To(Succeed())
	return count > 0
}

func inspectCatalog(ctx context.Context, db *sql.DB) {
	bounds := &inspector.Bounds{Rows: 1000, DefinitionBytes: 1 << 20}
	catalog, err := inspector.NewInspector(db, "sqlserver", bounds)
	Expect(err).ToNot(HaveOccurred())
	tables, err := catalog.GetTables(ctx, "dbo", "table")
	Expect(err).ToNot(HaveOccurred())
	Expect(tableNames(tables)).To(ConsistOf("acme_orders", "acme_order_lines"))
	indexes, err := catalog.GetIndexes(ctx, "dbo")
	Expect(err).ToNot(HaveOccurred())
	Expect(indexes).To(ContainElement(HaveField("IndexName", "IX_acme_orders_status")))
	fks, err := catalog.GetForeignKeys(ctx, "dbo")
	Expect(err).ToNot(HaveOccurred())
	Expect(fks).To(ContainElement(HaveField("ConstraintName", "FK_acme_order_lines_order")))
	procs, err := catalog.GetStoredProcs(ctx, "dbo")
	Expect(err).ToNot(HaveOccurred())
	Expect(procs).To(ContainElements(HaveField("Name", "acme_count_orders"), HaveField("Name", "acme_order_total")))
	params, err := catalog.GetProcParams(ctx, "dbo")
	Expect(err).ToNot(HaveOccurred())
	Expect(params).ToNot(BeEmpty())
	Expect(bounds.Truncated).To(BeFalse())
}

func tableNames(tables []*inspector.Table) []string {
	names := make([]string, len(tables))
	for i, t := range tables {
		names[i] = t.Name
	}
	return names
}

// sqlServerDatabase creates a database for one spec, at the given
// compatibility level when it is not zero, and drops it afterwards.
func sqlServerDatabase(ctx context.Context, adminURL string, compatibilityLevel int) (string, *sql.DB) {
	admin, err := sql.Open("sqlserver", adminURL)
	Expect(err).ToNot(HaveOccurred())
	DeferCleanup(admin.Close)
	name := fmt.Sprintf("commons_db_test_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(ctx, "CREATE DATABASE ["+name+"]")
	Expect(err).ToNot(HaveOccurred())
	DeferCleanup(func(ctx SpecContext) {
		_, err := admin.ExecContext(ctx, "ALTER DATABASE ["+name+"] SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE ["+name+"]")
		Expect(err).ToNot(HaveOccurred())
	})
	if compatibilityLevel != 0 {
		_, err = admin.ExecContext(ctx, fmt.Sprintf("ALTER DATABASE [%s] SET COMPATIBILITY_LEVEL = %d", name, compatibilityLevel))
		Expect(err).ToNot(HaveOccurred())
	}
	u, err := url.Parse(adminURL)
	Expect(err).ToNot(HaveOccurred())
	query := u.Query()
	query.Set("database", name)
	u.RawQuery = query.Encode()
	db, err := sql.Open("sqlserver", u.String())
	Expect(err).ToNot(HaveOccurred())
	DeferCleanup(db.Close)
	return u.String(), db
}

// sqlServerAdminURL returns the configured disposable server.
func sqlServerAdminURL(ctx context.Context) string {
	if configured := strings.TrimSpace(os.Getenv("COMMONS_DB_SQLSERVER_URL")); configured != "" {
		waitForSQLServer(ctx, configured)
		return configured
	}
	Skip("set COMMONS_DB_SQLSERVER_URL to a disposable SQL Server instance")
	return ""
}

func waitForSQLServer(ctx context.Context, adminURL string) {
	db, err := sql.Open("sqlserver", adminURL)
	Expect(err).ToNot(HaveOccurred())
	defer db.Close() //nolint:errcheck
	Eventually(func(ctx context.Context) error { return db.PingContext(ctx) }).
		WithContext(ctx).WithTimeout(2 * time.Minute).WithPolling(2 * time.Second).Should(Succeed())
}

const (
	sqlServerDSN  = "sqlserver://sa:secret@localhost:1433?database=acme"
	purgeRoutine  = "CREATE OR ALTER PROCEDURE dbo.acme_purge_orders @before DATE AS BEGIN DELETE FROM dbo.acme_orders WHERE created_at < @before; END"
	ordersHCLFile = `schema "dbo" {}
table "acme_orders" {
  schema = schema.dbo
  column "id" {
    type = int
  }
  column "created_at" {
    type = date
  }
  primary_key {
    columns = [column.id]
  }
}
`
)

func file(content string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(content)} }

var _ = Describe("SQL Server routine files", func() {
	DescribeTable("loads one routine per file, named after the file",
		func(name, content string) {
			routines, err := loadRoutines(fstest.MapFS{"routines/" + name: file(content)}, "routines")
			Expect(err).ToNot(HaveOccurred())
			Expect(routines).To(HaveLen(1))
			Expect(routines[0].Name).To(Equal("acme_purge_orders"))
			Expect(routines[0].Text).To(Equal(purgeRoutine))
		},
		Entry("a bare definition", "acme_purge_orders.sql", purgeRoutine),
		Entry("a trailing GO", "acme_purge_orders.sql", purgeRoutine+"\nGO\n"),
	)

	DescribeTable("refuses a file that is not exactly one routine named after it",
		func(name, content, message string) {
			_, err := loadRoutines(fstest.MapFS{"routines/" + name: file(content)}, "routines")
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("a name mismatch", "acme_purge.sql", purgeRoutine,
			`routines/acme_purge.sql creates "acme_purge_orders": the file must be named after its routine`),
		Entry("two routines in two batches", "acme_purge_orders.sql", purgeRoutine+"\nGO\n"+purgeRoutine,
			"expected exactly one batch, found 2"),
		Entry("two routines in one batch", "acme_purge_orders.sql", purgeRoutine+"\n"+purgeRoutine,
			"sqlserver: parse routine"),
		Entry("a table", "acme_purge_orders.sql", "CREATE TABLE dbo.acme_purge_orders (id INT)",
			"makes no routine"),
	)

	It("refuses an empty routine directory", func() {
		_, err := loadRoutines(fstest.MapFS{"routines/README.md": file("acme")}, "routines")
		Expect(err).To(MatchError(ContainSubstring("no .sql files found")))
	})
})

var _ = Describe("SQL Server migration bundle", func() {
	bundle := func() fstest.MapFS {
		return fstest.MapFS{
			"db/schema.hcl":                     file(ordersHCLFile),
			"db/routines/acme_purge_orders.sql": file(purgeRoutine),
			"db/routines/acme_count_orders.sql": file("CREATE PROCEDURE acme_count_orders AS BEGIN SELECT COUNT(*) FROM dbo.acme_orders; END\nGO"),
		}
	}

	It("builds the desired dbo schema from HCL tables and routine files, which are not scripts", func() {
		realm, err := sqlServerDesired(bundle(), resolveOptions([]Option{WithDir("db"), WithRoutines("db/routines")}))
		Expect(err).ToNot(HaveOccurred())
		dbo := realm.Schemas[0]
		Expect(dbo.Name).To(Equal("dbo"))
		Expect(dbo.Tables).To(HaveLen(1))
		Expect([]string{dbo.Procs[0].Name, dbo.Procs[1].Name}).To(ConsistOf("acme_purge_orders", "acme_count_orders"))
		Expect(sqlServerInspectOptions(dbo, nil)).To(Equal(&schema.InspectOptions{
			Mode:   schema.InspectSchemas | schema.InspectTables | schema.InspectFuncs,
			Tables: []string{"acme_orders", dbo.Procs[0].Name, dbo.Procs[1].Name},
		}))
	})

	It("builds routines alone when the bundle has no HCL", func() {
		realm, err := sqlServerDesired(fstest.MapFS{"routines/acme_purge_orders.sql": file(purgeRoutine)},
			resolveOptions([]Option{WithRoutines("routines")}))
		Expect(err).ToNot(HaveOccurred())
		Expect(realm.Schemas[0].Procs).To(HaveLen(1))
		Expect(sqlServerInspectOptions(realm.Schemas[0], nil).Mode).To(Equal(schema.InspectSchemas | schema.InspectFuncs))
	})

	DescribeTable("refuses what SQL Server migrations do not support before connecting",
		func(filesystem fstest.MapFS, opts []Option, message string) {
			err := Apply(GinkgoT().Context(), sqlServerDSN, filesystem, opts...)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("an empty bundle", fstest.MapFS{}, nil, `declares no tables or routines`),
		Entry("a SQL script outside the routine directory",
			fstest.MapFS{"db/routines/acme_purge_orders.sql": file(purgeRoutine), "db/001_seed.sql": file("SELECT 1")},
			[]Option{WithDir("db"), WithRoutines("db/routines")}, "do not support SQL migration scripts, found 001_seed.sql"),
		Entry("another schema", fstest.MapFS{"db/schema.hcl": file(ordersHCLFile)},
			[]Option{WithDir("db"), WithSchema("tenant_x")}, `support only schema "dbo", got "tenant_x"`),
		Entry("drops", fstest.MapFS{"db/schema.hcl": file(ordersHCLFile)},
			[]Option{WithDir("db"), WithDrops()}, "do not support destructive changes"),
		Entry("HCL in another schema", fstest.MapFS{"db/schema.hcl": file(`schema "tenant_x" {}`)},
			[]Option{WithDir("db")}, `must declare exactly schema "dbo", got ["tenant_x"]`),
	)

	DescribeTable("WithRoutines is refused by the other dialects",
		func(dsn, message string) {
			err := Apply(GinkgoT().Context(), dsn, fstest.MapFS{"db/schema.hcl": file(ordersHCLFile)},
				WithDir("db"), WithRoutines("db/routines"))
			Expect(err).To(MatchError(message))
		},
		Entry("PostgreSQL", "postgres://acme@localhost:5432/acme",
			"PostgreSQL migrations do not support routines (WithRoutines); only SQL Server does"),
		Entry("SQLite, before the file is opened", "sqlite://acme-never-created.db",
			"SQLite migrations do not support routines (WithRoutines); only SQL Server does"),
	)

	It("keeps undeclared routines and indexes of declared tables", func() {
		table := schema.NewTable("acme_orders")
		kept := &schema.AddColumn{C: schema.NewIntColumn("priority", "int")}
		changes := withoutSQLServerDrops([]schema.Change{
			&schema.DropProc{P: &schema.Proc{Name: "acme_legacy"}},
			&schema.DropFunc{F: &schema.Func{Name: "acme_legacy_total"}},
			&schema.ModifyTable{T: table, Changes: []schema.Change{&schema.DropIndex{I: schema.NewIndex("IX_dba_tuning")}, kept}},
			&schema.ModifyTable{T: table, Changes: []schema.Change{&schema.DropIndex{I: schema.NewIndex("IX_dba_other")}}},
		})
		Expect(changes).To(Equal([]schema.Change{&schema.ModifyTable{T: table, Changes: []schema.Change{kept}}}))
	})
})
