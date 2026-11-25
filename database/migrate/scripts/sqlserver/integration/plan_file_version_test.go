//go:build integration

package integration_test

import (
	"database/sql"
	"encoding/json"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	backend "github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver/sqlservertest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("A version 2 plan file on a disposable SQL Server", Ordered, func() {
	var db *sql.DB
	BeforeAll(func(ctx SpecContext) {
		db = sqlservertest.OpenDisposable(ctx)
		Expect(execSQL(ctx, db, "CREATE TABLE dbo.VersionRuns (Script nvarchar(40) NOT NULL)")).To(Succeed())
	})

	// resealed is what an older writer produced: the same file at version 2,
	// hashed over its own JSON.
	resealed := func(file backend.PlanFile) backend.PlanFile {
		GinkgoHelper()
		file.Version, file.Hash = 2, ""
		encoded, err := json.Marshal(file)
		Expect(err).NotTo(HaveOccurred())
		file.Hash = scripts.SourceHash(string(encoded))
		return file
	}

	It("applies as before when it has no schema step", func(ctx SpecContext) {
		file := plan(ctx, backend.Options{DB: db, Scope: "version2", Scripts: []scripts.Script{
			{ID: "stamp", Phase: scripts.Post, SQL: "INSERT dbo.VersionRuns VALUES (N'stamp')"},
		}})
		legacy := resealed(file)
		encoded, err := json.Marshal(legacy)
		Expect(err).NotTo(HaveOccurred())
		var decoded backend.PlanFile
		Expect(json.Unmarshal(encoded, &decoded)).To(Succeed())
		Expect(decoded.Verify()).To(Succeed())
		Expect(decoded.Version).To(Equal(2))

		Expect(apply(ctx, db, decoded).Applied).To(Equal([]string{"stamp"}))
		var count int
		Expect(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dbo.VersionRuns").Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))
	})
})
