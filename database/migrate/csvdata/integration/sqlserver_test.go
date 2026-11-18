//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"testing/fstest"
	"time"

	"github.com/flanksource/sqlglot-go/database/migrate/csvdata"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	scriptserver "github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver/sqlservertest"
	"github.com/flanksource/sqlglot-go/database/migrate/workflow"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCSVSQLServer(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "CSV migration SQL Server")
}

var _ = Describe("reviewed CSV changes", Ordered, func() {
	var db *sql.DB
	BeforeAll(func(ctx SpecContext) { db = sqlservertest.OpenDisposable(ctx) }, NodeTimeout(3*time.Minute))

	It("applies qualified inferred tables, layer tombstones and typed values exactly once", func(ctx SpecContext) {
		_, err := db.ExecContext(ctx, `CREATE TABLE dbo.AsCode (ID int PRIMARY KEY, Label nvarchar(100) NULL, Optional nvarchar(100) DEFAULT 'default');
INSERT INTO dbo.AsCode (ID,Label) VALUES (1,N'old'),(2,N'remove'),(7,N'unrelated');`)
		Expect(err).NotTo(HaveOccurred())
		files := fstest.MapFS{
			"AS.csv.yaml": {Data: []byte(`version: 1
defaults:
  schema: dbo
  tablePattern: '^(?P<table>AsCode)(_[A-Z]{2})?\.csv$'
layers:
  country: {values: [BW], match: ['**/*_${value}.csv'], valuePattern: '[A-Z]{2}'}
`)},
			"AsCode.csv":    {Data: []byte("ID,Label\n1,shared\n2,shared\n3,\"\\N\"\n4,\\N\n5,\"\"\n")},
			"AsCode_BW.csv": {Data: []byte("ID,Label,$delete\n1,regional,false\n2,,true\n")},
		}
		opts := csvdata.Options{Files: files, Schema: "dbo", Filters: map[string]string{"country": "none,BW"}}
		plan, err := workflow.Plan(ctx, db, workflow.Options{Dialect: "sqlserver", Schema: "dbo", CSV: opts})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.CSVPlan.Tables).To(ConsistOf(MatchFieldsForCounts(3, 1, 1, 0, 0)))
		Expect(string(plan.Artifact)).NotTo(ContainSubstring("regional"))
		_, err = workflow.Apply(ctx, db, workflow.ApplyOptions{Dialect: "sqlserver", Schema: "dbo", Artifact: plan.Artifact, Hash: plan.Hash, Acks: plan.Acks, CSV: opts, LockTimeout: time.Second})
		Expect(err).NotTo(HaveOccurred())

		var values string
		Expect(db.QueryRowContext(ctx, `SELECT ID,Label,Optional FROM dbo.AsCode ORDER BY ID FOR JSON PATH, INCLUDE_NULL_VALUES`).Scan(&values)).To(Succeed())
		Expect(values).To(MatchJSON(`[{"ID":1,"Label":"regional","Optional":"default"},{"ID":3,"Label":"\\N","Optional":"default"},{"ID":4,"Label":null,"Optional":"default"},{"ID":5,"Label":"","Optional":"default"},{"ID":7,"Label":"unrelated","Optional":"default"}]`))
		next, err := csvdata.Plan(ctx, db, opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(next.Tables).To(ConsistOf(MatchFieldsForCounts(0, 0, 0, 0, 5)))
	})

	It("rejects same-count snapshot delete drift and rolls back a failed phase", func(ctx SpecContext) {
		_, err := db.ExecContext(ctx, `CREATE TABLE dbo.AsSnapshot (ID int PRIMARY KEY);
INSERT INTO dbo.AsSnapshot VALUES (1),(2);
CREATE TABLE dbo.ZConstraint (ID int PRIMARY KEY, Score int CHECK (Score > 0));`)
		Expect(err).NotTo(HaveOccurred())
		files := fstest.MapFS{
			"AS.csv.yaml": {Data: []byte(`version: 1
tables:
  dbo.AsSnapshot:
    sources: [AsSnapshot.csv]
    delete: snapshot
    snapshotFiles: [AsSnapshot.csv]
  dbo.ZConstraint:
    sources: [ZConstraint.csv]
`)},
			"AsSnapshot.csv":  {Data: []byte("ID\n1\n3\n")},
			"ZConstraint.csv": {Data: []byte("ID,Score\n1,-1\n")},
		}
		opts := csvdata.Options{Files: files, Schema: "dbo"}
		plan, err := csvdata.Plan(ctx, db, opts)
		Expect(err).NotTo(HaveOccurred())
		request := csvdata.ApplyOptions{Options: opts, Artifact: plan, Hash: plan.Hash, Acks: plan.Acknowledgements(), LockTimeout: time.Second}
		_, err = csvdata.Apply(ctx, db, request)
		Expect(err).To(MatchError(ContainSubstring("CSV insert rejected")))
		Expect(snapshotRows(ctx, db)).To(MatchJSON("[1,2]"))
		_, err = db.ExecContext(ctx, "UPDATE dbo.AsSnapshot SET ID=4 WHERE ID=2")
		Expect(err).NotTo(HaveOccurred())
		_, err = csvdata.Apply(ctx, db, request)
		Expect(err).To(MatchError(ContainSubstring("row state changed")))
		Expect(snapshotRows(ctx, db)).To(MatchJSON("[1,4]"))
	})

	It("preserves SQL Server conversions across bulk staging and rolls back invalid values", func(ctx SpecContext) {
		_, err := db.ExecContext(ctx, `CREATE TABLE dbo.TypedBulk (
ID int PRIMARY KEY, Label varchar(30) COLLATE Latin1_General_100_CI_AS_SC_UTF8,
Amount decimal(19,4), Effective datetime2(7), Enabled bit, Payload varbinary(8));`)
		Expect(err).NotTo(HaveOccurred())
		const header = "ID,Label,Amount,Effective,Enabled,Payload\n"
		files := fstest.MapFS{
			"TypedBulk.csv.yaml": {Data: []byte("version: 1\ntable: dbo.TypedBulk\n")},
			"TypedBulk.csv":      {Data: []byte(header + "19,café,confidential,2026-11-18 13:40:00.1234567,true,0xC0FFEE\n")},
		}
		options := csvdata.Options{Files: files, Schema: "dbo"}
		_, err = csvdata.Plan(ctx, db, options)
		Expect(err).To(MatchError(ContainSubstring("convert staged CSV values")))
		Expect(err.Error()).NotTo(ContainSubstring("confidential"))
		files["TypedBulk.csv"] = &fstest.MapFile{Data: []byte(header + "19,café,123456789012345.679,2026-11-18 13:40:00.1234567,true,0xC0FFEE\n")}
		plan, err := csvdata.Plan(ctx, db, options)
		Expect(err).NotTo(HaveOccurred())
		_, err = csvdata.Apply(ctx, db, csvdata.ApplyOptions{Options: options, Artifact: plan, Hash: plan.Hash, LockTimeout: time.Second})
		Expect(err).NotTo(HaveOccurred())

		var values string
		Expect(db.QueryRowContext(ctx, `SELECT CONCAT(ID,N'|',Label,N'|',Amount,N'|',
CONVERT(nvarchar(30),Effective,126),N'|',Enabled,N'|',CONVERT(varchar(20),Payload,1))
FROM dbo.TypedBulk`).Scan(&values)).To(Succeed())
		Expect(values).To(Equal("19|café|123456789012345.6790|2026-11-18T13:40:00.1234567|1|0xC0FFEE"))
	})

	It("invalidates a script after a CSV update changes its function result", func(ctx SpecContext) {
		_, err := db.ExecContext(ctx, `CREATE TABLE dbo.CountryLookup (ID int PRIMARY KEY, Name nvarchar(40));
INSERT dbo.CountryLookup VALUES (1,N'Shared');
CREATE TABLE dbo.ObservedCountries (Name nvarchar(40));`)
		Expect(err).NotTo(HaveOccurred())
		_, err = db.ExecContext(ctx, `CREATE FUNCTION dbo.get_country_name() RETURNS nvarchar(40) AS
BEGIN RETURN (SELECT Name FROM dbo.CountryLookup WHERE ID=1); END`)
		Expect(err).NotTo(HaveOccurred())
		session, err := scriptserver.Open(ctx, scriptserver.Options{
			DB: db, Scope: "csv-lookup",
			Functions: []scriptserver.Function{{Name: "dbo.get_country_name", Type: "nvarchar"}},
			Scripts: []scripts.Script{{ID: "lookup", Phase: scripts.Post,
				SQL: "INSERT dbo.ObservedCountries VALUES (dbo.get_country_name());"}},
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(session.Close)
		first, err := session.Inspect(ctx, scripts.Post)
		Expect(err).NotTo(HaveOccurred())
		_, err = session.Apply(ctx, scriptserver.ApplyOptions{Preview: first})
		Expect(err).NotTo(HaveOccurred())
		csv := csvdata.Options{Schema: "dbo", Files: fstest.MapFS{
			"CountryLookup.csv.yaml": {Data: []byte("version: 1\ntable: dbo.CountryLookup\n")},
			"CountryLookup.csv":      {Data: []byte("ID,Name\n1,Regional\n")},
		}}
		plan, err := csvdata.Plan(ctx, db, csv)
		Expect(err).NotTo(HaveOccurred())
		_, err = csvdata.Apply(ctx, db, csvdata.ApplyOptions{Options: csv, Artifact: plan, Hash: plan.Hash, LockTimeout: time.Second})
		Expect(err).NotTo(HaveOccurred())
		next, err := session.Inspect(ctx, scripts.Post)
		Expect(err).NotTo(HaveOccurred())
		Expect(next.Plan.Scripts[0].Reasons).To(ContainElement("function-result-changed"))
	})
})

func MatchFieldsForCounts(inserts, updates, explicit, snapshot, unchanged int) any {
	return WithTransform(func(plan csvdata.TablePlan) []int {
		return []int{plan.Inserts, plan.Updates, plan.ExplicitDeletes, plan.SnapshotDeletes, plan.Unchanged}
	}, Equal([]int{inserts, updates, explicit, snapshot, unchanged}))
}

func snapshotRows(ctx context.Context, db *sql.DB) string {
	rows, err := db.QueryContext(ctx, "SELECT ID FROM dbo.AsSnapshot ORDER BY ID")
	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(rows.Close()).To(Succeed()) }()
	ids := []int{}
	for rows.Next() {
		var id int
		Expect(rows.Scan(&id)).To(Succeed())
		ids = append(ids, id)
	}
	Expect(rows.Err()).NotTo(HaveOccurred())
	body, err := json.Marshal(ids)
	Expect(err).NotTo(HaveOccurred())
	return string(body)
}
