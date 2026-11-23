package migrate

import (
	"strings"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	backend "github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL Server script batches", func() {
	DescribeTable("split at GO the way sqlcmd does",
		func(content string, want []string) {
			Expect(backend.Batches("seed.sql", content)).To(Equal(want))
		},
		Entry("one batch", "INSERT INTO dbo.acme VALUES (1);\n", []string{"INSERT INTO dbo.acme VALUES (1);"}),
		Entry("GO separators, a trailing GO dropped",
			"CREATE TABLE dbo.acme (id INT);\nGO\nINSERT INTO dbo.acme VALUES (1);\ngo\n",
			[]string{"CREATE TABLE dbo.acme (id INT);", "INSERT INTO dbo.acme VALUES (1);"}),
		Entry("GO inside a comment or string is not a separator",
			"/*\nGO\n*/\nSELECT 'a\nGO\nb';",
			[]string{"/*\nGO\n*/\nSELECT 'a\nGO\nb';"}),
		Entry("a quoted GO beginning a line", "SELECT\n'GO';\nGO\nSELECT 2", []string{"SELECT\n'GO';", "SELECT 2"}),
		Entry("SQLCMD text inside a block comment", "/*\n:r missing.sql\n$(Missing)\n*/\nSELECT 1;", []string{"/*\n:r missing.sql\n$(Missing)\n*/\nSELECT 1;"}),
		Entry("SQLCMD text inside a line comment", "-- $(Missing)\nSELECT 1;", []string{"-- $(Missing)\nSELECT 1;"}),
	)

	DescribeTable("refuse what sqlcmd and SSMS would read differently",
		func(content, message string) {
			_, err := backend.Batches("seed.sql", content)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry(":setvar", ":setvar Country ZW\nSELECT 1", `line 1: sqlcmd command ":setvar Country ZW" is not supported`),
		Entry("a shell command", "SELECT 1\n  !! dir", `line 2: sqlcmd command "!! dir" is not supported`),
		Entry("a scripting variable, even in a string", "SELECT 1\nSELECT '$(Country)'", "line 2: sqlcmd scripting variables"),
		Entry("a scripting variable outside a string", "SELECT $(Country)", "line 1: sqlcmd scripting variables"),
		Entry("malformed GO", "GO 2\n", "GO must stand alone"),
		Entry("unterminated block comment", "/* disabled", "split batches:"),
	)
	DescribeTable("accepts a script with no executable batches",
		func(content string) {
			batches, err := backend.Batches("seed.sql", content)
			Expect(err).NotTo(HaveOccurred())
			Expect(batches).To(BeEmpty())
		},
		Entry("blank SQL", "\r\n \t"),
		Entry("comments only", "/*\r\nDELETE FROM dbo.example;\r\n*/"),
		Entry("only separators and comments", "-- disabled\nGO\n"),
		Entry("only empty statements", ";\nGO\n;;"),
	)
	It("reads a leading migration directive followed by a SQL line larger than 64 KiB", func() {
		content := "-- phase: pre\nSELECT N'" + strings.Repeat("a", 1<<16) + "';"
		script, err := scripts.Parse("seed.sql", content)
		Expect(err).NotTo(HaveOccurred())
		Expect(script.Phase).To(Equal(scripts.Pre))
		Expect(script.SQL).To(Equal(content))
	})
})
