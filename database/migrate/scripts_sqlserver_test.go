package migrate

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL Server script batches", func() {
	DescribeTable("split at GO the way sqlcmd does",
		func(content string, want []string) {
			Expect(sqlcmdBatches("seed.sql", content)).To(Equal(want))
		},
		Entry("one batch", "INSERT INTO dbo.acme VALUES (1);\n", []string{"INSERT INTO dbo.acme VALUES (1);"}),
		Entry("GO separators, a trailing GO dropped",
			"CREATE TABLE dbo.acme (id INT);\nGO\nINSERT INTO dbo.acme VALUES (1);\ngo\n",
			[]string{"CREATE TABLE dbo.acme (id INT);", "INSERT INTO dbo.acme VALUES (1);"}),
		Entry("GO inside a comment or string is not a separator",
			"/*\nGO\n*/\nSELECT 'a\nGO\nb';",
			[]string{"/*\nGO\n*/\nSELECT 'a\nGO\nb';"}),
	)

	DescribeTable("refuse what sqlcmd and SSMS would read differently",
		func(content, message string) {
			_, err := sqlcmdBatches("seed.sql", content)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry(":setvar", ":setvar Country ZW\nSELECT 1", `line 1: sqlcmd command ":setvar Country ZW" is not supported`),
		Entry("a shell command", "SELECT 1\n  !! dir", `line 2: sqlcmd command "!! dir" is not supported`),
		Entry("a scripting variable, even in a string", "SELECT 1\nSELECT '$(Country)'", "line 2: sqlcmd scripting variables"),
		Entry("only GO", "GO\n", "has no statements"),
	)
})
