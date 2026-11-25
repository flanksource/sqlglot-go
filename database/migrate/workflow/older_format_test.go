package workflow

import (
	"context"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	emptySchema = `{"tables":[],"definitions":[]}`
	// versionOneArtifact is a SQL Server artifact as migrate.Artifact version 1 wrote it.
	versionOneArtifact = `{"version":1,"schemas":["dbo"],"request":{"current":` + emptySchema + `,"desired":` + emptySchema + `},"plan":{"steps":[],"changes":[]},"hash":"1f"}`
	// reviewArtifact is a PostgreSQL or SQLite artifact as the review package wrote it.
	reviewArtifact = `{"version":2,"baseline":{"dialect":"sqlite","schema":"main","hcl":"schema \"main\" {}"},` +
		`"desired":{"dialect":"sqlite","schema":"main","hcl":"schema \"main\" {}"},"steps":[],"hash":"2f"}`
)

var _ = Describe("a plan file made by an older version", func() {
	DescribeTable("is refused with a request to plan again, whichever way it is read", func(file string) {
		ctx := context.Background()
		db := openSQLite(filepath.Join(GinkgoT().TempDir(), "older.db"))

		_, err := Review([]byte(file), "", "")
		Expect(err).To(MatchError(ContainSubstring("made by an older version")))
		Expect(err).To(MatchError(ContainSubstring("must be re-planned")))
		Expect(err).To(MatchError(ErrOlderVersion))

		err = Validate(ApplyOptions{Artifact: []byte(file)})
		Expect(err).To(MatchError(ErrOlderVersion))
		_, err = Apply(ctx, db, ApplyOptions{Dialect: "sqlite", Schema: "main", Artifact: []byte(file)})
		Expect(err).To(MatchError(ErrOlderVersion))
		_, err = Plan(ctx, db, Options{Dialect: "sqlite", Schema: "main", Source: Source{Kind: "artifact", Content: file}})
		Expect(err).To(MatchError(ErrOlderVersion))
	},
		Entry("a version 1 SQL Server artifact", versionOneArtifact),
		Entry("a PostgreSQL or SQLite artifact with HCL snapshots", reviewArtifact),
		Entry("a bundle preview wrapping a review artifact", `{"artifact":`+reviewArtifact+`,"sources":[]}`),
		Entry("a bundle preview wrapping a version 1 SQL Server artifact", `{"sqlServerArtifact":`+versionOneArtifact+`}`),
		Entry("an advisory SQL Server preview", `{"sqlServerPlan":{"steps":[],"changes":[]},"warnings":["SQL scripts require phased reviewed artifacts"]}`),
	)

	It("names a file that is no artifact at all, rather than calling it old", func() {
		_, err := Review([]byte(`{"hello":"world"}`), "", "")
		Expect(err).To(MatchError(ContainSubstring("not a migration artifact")))
		Expect(err).NotTo(MatchError(ErrOlderVersion))
	})
})
