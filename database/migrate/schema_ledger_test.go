package migrate

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing/fstest"

	"github.com/flanksource/sqlglot-go/database/target"
	native "github.com/flanksource/sqlglot-go/migrate"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("schema ledger", func() {
	It("skips planning a SQLite schema it already applied, and applies one that changed", func(ctx context.Context) {
		dsn := filepath.Join(GinkgoT().TempDir(), "ledger.db")
		files := fstest.MapFS{"schema.hcl": &fstest.MapFile{Data: []byte(portableSchema)}}
		Expect(Apply(ctx, dsn, files)).To(Succeed())
		applyRecorded := func() []string {
			target, err := dbtarget.Parse(dsn)
			Expect(err).ToNot(HaveOccurred())
			recorder := registerRecordingDriver()
			database, err := sql.Open("sqlite-recording", target.DSN)
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(database.Close)
			before := len(recorder.recorded())
			Expect(applySQLite(ctx, database, files, resolveOptions(nil))).To(Succeed())
			return recorder.recorded()[before:]
		}

		Expect(applyRecorded()).To(HaveEach(ContainSubstring(schemaLedgerTable)))

		files["schema.hcl"] = &fstest.MapFile{Data: []byte(strings.Replace(portableSchema,
			`  column "created_at" {`,
			"  column \"description\" {\n    type = text\n    null = true\n  }\n  column \"created_at\" {", 1))}
		Expect(applyRecorded()).To(ContainElement("BEGIN IMMEDIATE"))
		Expect(applyRecorded()).To(HaveEach(ContainSubstring(schemaLedgerTable)))
	})

	base := func() schemaStep {
		return schemaStep{Dialect: "sqlite", Schema: "main", Desired: native.Schema{Native: `schema "main" {}`}}
	}
	DescribeTable("hashes everything that decides the plan", func(change func(*schemaStep, *[]string)) {
		step, scripts := base(), []string{"pre/001.sql:aa"}
		before, err := step.desiredHash(scripts)
		Expect(err).ToNot(HaveOccurred())
		change(&step, &scripts)
		after, err := step.desiredHash(scripts)
		Expect(err).ToNot(HaveOccurred())
		Expect(after).ToNot(Equal(before))
	},
		Entry("the desired schema", func(s *schemaStep, _ *[]string) { s.Desired.Native += "\n" }),
		Entry("the dialect", func(s *schemaStep, _ *[]string) { s.Dialect = "postgres" }),
		Entry("the managed schema", func(s *schemaStep, _ *[]string) { s.Schema = "tenant" }),
		Entry("the drop scope", func(s *schemaStep, _ *[]string) { s.AllowDrops = true }),
		Entry("the exclusions", func(s *schemaStep, _ *[]string) { s.Exclude = []string{"audit"} }),
		Entry("a script", func(_ *schemaStep, scripts *[]string) { *scripts = []string{"pre/001.sql:bb"} }),
	)
})
