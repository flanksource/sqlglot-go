//go:build integration

package integration_test

import (
	"encoding/hex"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	backend "github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver/sqlservertest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Verified migration history adoption", func() {
	It("blocks old records with newly tracked inputs until the postcondition is verified", func(ctx SpecContext) {
		db := sqlservertest.OpenDisposable(ctx)
		Expect(execSQL(ctx, db, `CREATE TABLE dbo.CountryContext (Name nvarchar(40) NOT NULL);
INSERT dbo.CountryContext VALUES (N'Zimbabwe');
CREATE TABLE dbo.PreviousOutput (Country nvarchar(40) NOT NULL);
INSERT dbo.PreviousOutput VALUES (N'Malawi');
CREATE TABLE dbo.sqlglot_migrations (
 scope nvarchar(256) NOT NULL, path nvarchar(512) NOT NULL, hash varbinary(32) NOT NULL,
 updated_at datetime2 NOT NULL DEFAULT SYSUTCDATETIME(),
 CONSTRAINT PK_sqlglot_migrations PRIMARY KEY NONCLUSTERED (scope,path));`)).To(Succeed())
		Expect(execSQL(ctx, db, `CREATE FUNCTION dbo.get_country_name() RETURNS nvarchar(40) AS
BEGIN RETURN (SELECT Name FROM dbo.CountryContext); END`)).To(Succeed())
		const script = "INSERT dbo.PreviousOutput VALUES (dbo.get_country_name());"
		hash := scripts.SourceHash(script)
		binaryHash, err := hex.DecodeString(hash)
		Expect(err).NotTo(HaveOccurred())
		_, err = db.ExecContext(ctx, "INSERT dbo.sqlglot_migrations(scope,path,hash) VALUES(N'adoption',N'seed',@p1)", binaryHash)
		Expect(err).NotTo(HaveOccurred())
		session, err := backend.Open(ctx, backend.Options{
			DB: db, Scope: "adoption", Scripts: []scripts.Script{{ID: "seed", Phase: scripts.Post, SQL: script}},
			Functions: []backend.Function{{Name: "dbo.get_country_name", Type: "nvarchar"}},
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(session.Close)
		before, err := session.Inspect(ctx, scripts.Post)
		Expect(err).NotTo(HaveOccurred())
		Expect(before.Plan.Scripts[0].Blocked).To(ContainElement("adoption-required"))
		evidence := []backend.AdoptionEvidence{{
			ID: "seed", SourceHash: hash, Provenance: "reviewed-output",
			Query: "SELECT Country FROM dbo.PreviousOutput", Expected: "Zimbabwe",
		}}
		artifact, err := backend.AdoptionPlan(before, evidence)
		Expect(err).NotTo(HaveOccurred())
		Expect(session.Adopt(ctx, backend.AdoptionOptions{Adoption: artifact})).To(MatchError(ContainSubstring("postcondition mismatch")))

		Expect(execSQL(ctx, db, "UPDATE dbo.PreviousOutput SET Country=N'Zimbabwe'")).To(Succeed())
		Expect(session.Adopt(ctx, backend.AdoptionOptions{Adoption: artifact})).To(Succeed())
		after, err := session.Inspect(ctx, scripts.Post)
		Expect(err).NotTo(HaveOccurred())
		Expect(after.Plan.Scripts[0].Blocked).To(BeEmpty())
		Expect(after.Plan.Scripts[0].Selected).To(BeFalse())
		var count int
		Expect(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dbo.PreviousOutput").Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1), "adoption must never execute the INSERT in the original script")
		AddReportEntry("C14", map[string]any{"initialBlock": before.Plan.Scripts[0].Blocked, "verifiedResult": evidence[0].Expected, "rowsAfterAdoption": count})
	})
})
