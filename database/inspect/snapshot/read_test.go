package snapshot

import (
	"errors"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/flanksource/sqlglot-go/migrate"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Migration inspection boundary", func() {
	It("rejects expression dependencies that the table migration model cannot preserve", func() {
		db, mock, err := sqlmock.New()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { mock.ExpectClose(); Expect(db.Close()).To(Succeed()) })
		mock.ExpectQuery(`dependent default, check or column expression`).WithArgs("dbo").WillReturnRows(sqlmock.NewRows([]string{"object", "feature"}).AddRow("DF_orders_id", "dependent default, check or column expression"))
		err = refuseUnsupported(GinkgoT().Context(), db, "dbo")
		Expect(err).To(MatchError(And(ContainSubstring("dbo.DF_orders_id"), ContainSubstring("migration snapshot is incomplete"))))
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})
	It("requires full definition visibility before enumerating objects", func() {
		db, mock, err := sqlmock.New()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { mock.ExpectClose(); Expect(db.Close()).To(Succeed()) })
		mock.ExpectQuery("HAS_PERMS_BY_NAME").WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(0))
		_, err = Inspect(GinkgoT().Context(), db, Options{Schemas: []string{"dbo"}})
		Expect(err).To(MatchError(ContainSubstring("VIEW DEFINITION")))
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})

	It("reads full view definitions, trigger state, module settings, sequences and table types without explorer limits", func() {
		db, mock, err := sqlmock.New()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { mock.ExpectClose(); Expect(db.Close()).To(Succeed()) })
		mock.ExpectQuery("HAS_PERMS_BY_NAME").WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(1))
		mock.ExpectQuery("INFORMATION_SCHEMA.SCHEMATA").WithArgs("dbo").WillReturnRows(sqlmock.NewRows([]string{"schema"}).AddRow("dbo"))
		viewSQL := "CREATE VIEW dbo.orders AS SELECT 1 AS id"
		triggerSQL := "CREATE TRIGGER dbo.audit_orders ON dbo.orders INSTEAD OF INSERT AS SELECT 1"
		mock.ExpectQuery(`OBJECT_DEFINITION\(o.object_id\)`).WithArgs("dbo", "").WillReturnRows(sqlmock.NewRows([]string{"schema", "name", "type", "definition"}).AddRow("dbo", "orders", "VIEW", viewSQL))
		for _, query := range []string{`FROM INFORMATION_SCHEMA.COLUMNS`, `FROM sys.indexes`, `FROM sys.index_columns`, `FROM sys.foreign_keys`, `FROM sys.check_constraints`, `FROM sys.objects o`, `FROM sys.columns c`, `i.fill_factor NOT IN`, `c.is_rowguidcol=1`, `dc.is_system_named=1`} {
			mock.ExpectQuery(query).WithArgs("dbo").WillReturnRows(sqlmock.NewRows([]string{"empty"}))
		}
		mock.ExpectQuery(`m.uses_quoted_identifier=0`).WithArgs("dbo").WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("orders"))
		mock.ExpectQuery(`FROM sys.sequences`).WithArgs("dbo").WillReturnRows(sqlmock.NewRows([]string{"name", "type", "user", "precision", "scale", "start", "increment", "min", "max", "cycle", "cached", "cache"}).
			AddRow("invoice_ids", "decimal", false, 18, 0, "1", "1", "1", "999999999999999999", false, true, 50))
		mock.ExpectQuery(`col.object_id=tt.type_table_object_id`).WithArgs("dbo").WillReturnRows(sqlmock.NewRows([]string{"type", "column", "data", "user", "max", "precision", "scale", "nullable", "identity", "seed", "increment", "default", "collation"}).
			AddRow("field_list", "id", "int", false, 4, 10, 0, false, false, nil, nil, nil, nil).
			AddRow("field_list", "kind", "nvarchar", false, 60, 0, 0, true, false, nil, nil, "(N'text')", "Latin1_General_CI_AS"))
		mock.ExpectQuery(`i.object_id=tt.type_table_object_id`).WithArgs("dbo").WillReturnRows(sqlmock.NewRows([]string{"type", "index", "primary", "unique", "desc", "column", "descending", "included"}).
			AddRow("field_list", 1, true, true, "CLUSTERED", "id", false, false))
		mock.ExpectQuery(`tt.type_table_object_id=cc.parent_object_id`).WithArgs("dbo").WillReturnRows(sqlmock.NewRows([]string{"type", "definition"}).
			AddRow("field_list", "([kind]<>N'')"))
		mock.ExpectQuery(`SCHEMA_NAME\(trigger_object.schema_id\)`).WithArgs("dbo").WillReturnRows(sqlmock.NewRows([]string{"schema", "name", "parentSchema", "parent", "disabled", "definition"}).AddRow("dbo", "audit_orders", "dbo", "orders", true, triggerSQL))
		mock.ExpectQuery(`FROM sys.sql_expression_dependencies`).WithArgs("dbo").WillReturnRows(sqlmock.NewRows([]string{"empty"}))
		mock.ExpectQuery(`SELECT object_name, feature`).WithArgs("dbo").WillReturnRows(sqlmock.NewRows([]string{"empty"}))
		result, err := Inspect(GinkgoT().Context(), db, Options{Schemas: []string{"dbo"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Schema.Definitions).To(Equal([]migrate.Definition{
			{Name: migrate.Name{Schema: "dbo", Name: "orders"}, Kind: "VIEW", SQL: viewSQL, QuotedIdentifierOff: true},
			{Name: migrate.Name{Schema: "dbo", Name: "audit_orders"}, Kind: "TRIGGER", SQL: triggerSQL, Parent: &migrate.Name{Schema: "dbo", Name: "orders"}, Disabled: true},
		}))
		Expect(result.Schema.Sequences).To(Equal([]migrate.Sequence{{Name: migrate.Name{Schema: "dbo", Name: "invoice_ids"}, Type: "decimal(18,0)",
			Start: "1", Increment: "1", Minimum: "1", Maximum: "999999999999999999", CacheSize: 50}}))
		Expect(result.Schema.TableTypes).To(Equal([]migrate.TableType{{Name: migrate.Name{Schema: "dbo", Name: "field_list"},
			Columns: []migrate.Column{{Name: "id", Type: "int"}, {Name: "kind", Type: "nvarchar(30)", Nullable: true, Default: &migrate.Default{Expression: "(N'text')"}, Collation: "Latin1_General_CI_AS"}},
			Keys:    []migrate.Index{{Primary: true, Clustered: true, Columns: []migrate.IndexColumn{{Name: "id"}}}},
			Checks:  []string{"([kind]<>N'')"}}}))
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})

	It("propagates catalogue read failures with their schema", func() {
		db, mock, err := sqlmock.New()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { mock.ExpectClose(); Expect(db.Close()).To(Succeed()) })
		mock.ExpectQuery("HAS_PERMS_BY_NAME").WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(1))
		mock.ExpectQuery("INFORMATION_SCHEMA.SCHEMATA").WillReturnError(errors.New("catalogue unavailable"))
		_, err = Inspect(GinkgoT().Context(), db, Options{Schemas: []string{"dbo"}})
		Expect(err).To(MatchError(And(ContainSubstring("dbo"), ContainSubstring("catalogue unavailable"))))
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})
})
