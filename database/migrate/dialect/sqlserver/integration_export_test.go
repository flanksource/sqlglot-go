//go:build integration

package sqlserver

import (
	"strings"

	"ariga.io/atlas/sql/schema"
	atlashcl "github.com/flanksource/sqlglot-go/database/atlas/sqlserver"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("exporting live tables as HCL", Label("integration"), func() {
	scope := declared("expo", "customers", "orders")

	BeforeEach(func(ctx SpecContext) {
		mustExec(ctx, db, "CREATE SCHEMA expo",
			`CREATE TABLE expo.customers (id int IDENTITY(5,10) NOT NULL CONSTRAINT PK_customers PRIMARY KEY NONCLUSTERED, region nvarchar(10) NULL)`,
			`CREATE TABLE expo.orders (
				id int NOT NULL, customer_id int NOT NULL,
				status nvarchar(20) NOT NULL CONSTRAINT DF_orders_status DEFAULT (N'new'),
				qty int NOT NULL DEFAULT (0), note nvarchar(max) NULL, amount decimal(18,2) NULL,
				label nvarchar(50) COLLATE Latin1_General_BIN NULL,
				CONSTRAINT PK_orders PRIMARY KEY NONCLUSTERED (id),
				CONSTRAINT UQ_orders_label UNIQUE (label),
				CONSTRAINT FK_orders_customers FOREIGN KEY (customer_id) REFERENCES expo.customers (id) ON DELETE CASCADE ON UPDATE NO ACTION,
				CONSTRAINT CK_orders_qty CHECK (qty >= 0))`,
			`CREATE CLUSTERED INDEX CIX_orders_customer ON expo.orders (customer_id, id)`,
			`CREATE INDEX IX_orders_status ON expo.orders (status, qty DESC) INCLUDE (amount, note) WHERE qty > 0`)
		DeferCleanup(func(ctx SpecContext) {
			mustExec(ctx, db, "DROP TABLE expo.orders", "DROP TABLE expo.customers", "DROP SCHEMA expo")
		})
	})

	declaredSchema := func(files []planner.HCLFile) []byte {
		GinkgoHelper()
		Expect(files).To(HaveLen(1))
		return []byte(files[0].Content)
	}

	It("plans the export of the live tables against the same database in declared scope with no change", func(ctx SpecContext) {
		files, err := ExportHCL(ctx, db, ExportOptions{Schema: "expo"})
		Expect(err).NotTo(HaveOccurred())
		var realm schema.Realm
		Expect(atlashcl.EvalHCLBytes(declaredSchema(files), &realm, nil)).To(Succeed())
		desired, err := planner.FromAtlas(&realm)
		Expect(err).NotTo(HaveOccurred())

		artifact, err := planLive(ctx, db, scope, desired)
		Expect(err).NotTo(HaveOccurred())
		Expect(artifact.Changes).To(BeEmpty())
		Expect(strings.Count(files[0].Content, "default_name")).To(Equal(1), "the system-named default carries no name")
	})

	It("exports one table, or fails for one that does not exist", func(ctx SpecContext) {
		files, err := ExportHCL(ctx, db, ExportOptions{Schema: "expo", Tables: []string{"customers"}, HCL: planner.HCLOptions{Layout: planner.HCLFilePerTable}})
		Expect(err).NotTo(HaveOccurred())
		Expect([]string{files[0].Name}).To(Equal([]string{"customers.hcl"}))
		Expect(files).To(HaveLen(1))
		_, err = ExportHCL(ctx, db, ExportOptions{Schema: "expo", Tables: []string{"missing"}})
		Expect(err).To(MatchError(ContainSubstring("table expo.missing does not exist")))
	})

	It("refuses an index option the HCL cannot declare, or names it in a comment when asked", func(ctx SpecContext) {
		mustExec(ctx, db, "ALTER INDEX IX_orders_status ON expo.orders REBUILD WITH (FILLFACTOR = 80)")
		_, err := ExportHCL(ctx, db, ExportOptions{Schema: "expo"})
		Expect(err).To(MatchError(ContainSubstring("table expo.orders, index IX_orders_status: fill factor 80")))
		files, err := ExportHCL(ctx, db, ExportOptions{Schema: "expo", HCL: planner.HCLOptions{OmitUnsupported: true}})
		Expect(err).NotTo(HaveOccurred())
		Expect(files[0].Content).To(ContainSubstring("# omitted: table expo.orders, index IX_orders_status: fill factor 80 cannot be declared"))
	})
})
