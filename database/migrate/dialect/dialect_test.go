package dialect_test

import (
	"testing"

	"github.com/flanksource/sqlglot-go/database/migrate/dialect"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestDialect(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Reviewed workflow dialect helpers")
}

var _ = Describe("desired schema from HCL", func() {
	const sqlServerHCL = `schema "app" {}
table "orders" {
  schema = schema.app
  column "id" { type = int }
  column "ref" { type = uniqueidentifier }
}`
	const sqliteHCL = `schema "main" {}
table "items" {
  schema = schema.main
  column "id" { type = integer }
}`

	DescribeTable("reads each dialect's own HCL flavour", func(name, hcl, schemaName, table string, columns int) {
		desired, err := dialect.DesiredFromHCL(name, hcl)
		Expect(err).NotTo(HaveOccurred())
		Expect(desired.Tables).To(HaveLen(1))
		Expect(desired.Tables[0].Name).To(Equal(migrate.Name{Schema: schemaName, Name: table}))
		Expect(desired.Tables[0].Columns).To(HaveLen(columns))
	},
		Entry("SQL Server", "sqlserver", sqlServerHCL, "app", "orders", 2),
		Entry("SQLite", "sqlite", sqliteHCL, "main", "items", 1),
	)

	It("refuses a dialect that reads no HCL and empty HCL", func() {
		_, err := dialect.DesiredFromHCL("mysql", sqliteHCL)
		Expect(err).To(MatchError(ContainSubstring(`dialect "mysql" cannot read desired HCL (supported: postgres, sqlite, sqlserver)`)))
		_, err = dialect.DesiredFromHCL("sqlite", " \n")
		Expect(err).To(MatchError(ContainSubstring("HCL is empty")))
	})
})

var _ = Describe("a snapshot used as the desired state", func() {
	table := migrate.Table{Name: migrate.Name{Schema: "main", Name: "items"}}
	snapshot := dialect.Snapshot{Dialect: "sqlite", Schemas: []string{"main"}, Schema: migrate.Schema{Tables: []migrate.Table{table}}}

	It("must cover exactly the target's schemas and hold nothing outside them", func() {
		Expect(snapshot.CheckWithin([]string{"main"})).To(Succeed())
		Expect(snapshot.CheckWithin([]string{"other"})).To(MatchError(ContainSubstring("covers schemas [main], not the target's [other]")))
		stray := snapshot
		stray.Schema = migrate.Schema{Tables: []migrate.Table{{Name: migrate.Name{Schema: "other", Name: "t"}}}}
		Expect(stray.CheckWithin([]string{"main"})).To(MatchError(ContainSubstring("desired object other.t is outside the schemas [main]")))
	})

	It("fails a plan that touches an object it refused, and no other", func() {
		refused := snapshot
		refused.Refusals = []engine.Refusal{{Object: table.Name, Member: "idx", Reason: "partial index"}}
		drop := migrate.Change{ID: "drop-idx", Object: table.Name, Member: "idx"}
		other := migrate.Change{ID: "drop-col", Object: table.Name, Member: "col"}
		Expect(refused.RefuseTouched([]migrate.Change{other})).To(Succeed())
		Expect(refused.RefuseTouched([]migrate.Change{other, drop})).To(MatchError(ContainSubstring("change drop-idx touches main.items, which the reference snapshot refused: partial index")))
	})

	It("counts tables and every other kind that is present", func() {
		s := dialect.Snapshot{Schema: migrate.Schema{
			Tables:      []migrate.Table{table},
			Definitions: []migrate.Definition{{Kind: "VIEW"}, {Kind: "view"}, {Kind: "PROCEDURE"}},
			Sequences:   []migrate.Sequence{{}},
		}}
		Expect(s.Summary()).To(Equal(map[string]int{"table": 1, "view": 2, "procedure": 1, "sequence": 1}))
		Expect(dialect.Snapshot{}.Summary()).To(Equal(map[string]int{"table": 0}))
	})
})

var _ = Describe("a script of statements", func() {
	It("ends each statement the way its dialect needs", func() {
		sqlServer, err := dialect.Script("sqlserver", []string{"CREATE VIEW v AS SELECT 1;", "DROP TABLE t"})
		Expect(err).NotTo(HaveOccurred())
		Expect(sqlServer).To(Equal("CREATE VIEW v AS SELECT 1\nGO\nDROP TABLE t\nGO\n"))
		sqlite, err := dialect.Script("sqlite", []string{"DROP TABLE t"})
		Expect(err).NotTo(HaveOccurred())
		Expect(sqlite).To(Equal("DROP TABLE t;\n"))
		_, err = dialect.Script("mysql", nil)
		Expect(err).To(MatchError(ContainSubstring("no script format")))
	})
})
