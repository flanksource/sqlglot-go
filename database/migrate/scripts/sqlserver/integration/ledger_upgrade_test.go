//go:build integration

package integration_test

import (
	"errors"
	"slices"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	backend "github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver"
	"github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver/sqlservertest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Absorbing ledger scopes", func() {
	It("merges two repository scopes into one, newest run per path, and re-runs the script on a body switch", func(ctx SpecContext) {
		db := sqlservertest.OpenDisposable(ctx)
		Expect(execSQL(ctx, db, "CREATE TABLE dbo.SeedRuns (Country nvarchar(40) NOT NULL);")).To(Succeed())
		const merged = "mission-control-oipa"
		repositories := []string{"dev0base", "zim"}
		bodies := map[string]string{"zim": "INSERT dbo.SeedRuns VALUES (N'ZW');", "dev0base": "INSERT dbo.SeedRuns VALUES (N'BASE');"}
		bundle := func(scope, body string) backend.Options {
			opts := backend.Options{DB: db, Scope: scope, Scripts: []scripts.Script{{ID: "seed", Phase: scripts.Post, SQL: body}}}
			if scope == merged {
				opts.AbsorbScopes = func(other string) bool { return slices.Contains(repositories, other) }
			}
			return opts
		}
		for _, repository := range []string{"zim", "dev0base"} {
			Expect(apply(ctx, db, plan(ctx, bundle(repository, bodies[repository]))).Applied).To(Equal([]string{"seed"}))
		}
		lastRun := plan(ctx, bundle("dev0base", bodies["dev0base"])).Ledger["seed"]

		before := plan(ctx, bundle(merged, bodies["dev0base"]))
		Expect(before.Absorbed).To(Equal(repositories))
		Expect(before.Selected()).To(BeEmpty(), "dev0base ran last, with this body")
		Expect(before.Ledger["seed"]).To(Equal(lastRun))

		Expect(apply(ctx, db, plan(ctx, bundle("zim", "INSERT dbo.SeedRuns VALUES (N'ZW2');"))).Applied).To(Equal([]string{"seed"}))
		_, err := backend.Apply(ctx, db, before)
		var stale *backend.StalePlanError
		Expect(errors.As(err, &stale)).To(BeTrue(), "an absorbed scope's newer run moves the plan")

		for _, repository := range []string{"dev0base", "zim", "dev0base"} {
			switched := plan(ctx, bundle(merged, bodies[repository]))
			Expect(switched.Actions[0].Reasons).To(Equal([]string{"source-changed"}))
			Expect(apply(ctx, db, switched).Applied).To(Equal([]string{"seed"}))
		}
		after := plan(ctx, bundle(merged, bodies["dev0base"]))
		Expect(after.Absorbed).To(BeEmpty(), "nothing is left to absorb")
		Expect(after.Selected()).To(BeEmpty())

		var ledgerScopes, ledgerRows, attemptScopes, foreignObservations int
		Expect(db.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(DISTINCT scope) FROM dbo.sqlglot_migrations),
 (SELECT COUNT(*) FROM dbo.sqlglot_migrations WHERE scope=@p1),
 (SELECT COUNT(DISTINCT scope) FROM dbo.sqlglot_migration_attempts),
 (SELECT COUNT(*) FROM dbo.sqlglot_observations WHERE scope<>@p1)`, merged).
			Scan(&ledgerScopes, &ledgerRows, &attemptScopes, &foreignObservations)).To(Succeed())
		Expect([]int{ledgerScopes, ledgerRows, attemptScopes, foreignObservations}).To(Equal([]int{1, 1, 1, 0}))
	})
})
