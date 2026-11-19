package sqlglot

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("T-SQL deployment syntax", func() {
	DescribeTable("parses and generates typed deployment statements",
		func(sql, class, generatedFragment string) {
			result, err := ParseScript(sql, "tsql")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Diagnostics).To(BeEmpty())
			Expect(result.Complete).To(BeTrue())
			Expect(result.Tree).To(HaveField("Class", class))

			generated, err := Generate(result.Tree, "tsql")
			Expect(err).NotTo(HaveOccurred())
			Expect(generated).To(ContainSubstring(generatedFragment))
			reparsed, err := ParseScript(generated, "tsql")
			Expect(err).NotTo(HaveOccurred())
			Expect(reparsed.Diagnostics).To(BeEmpty())
			Expect(reparsed.Tree.Dump()).To(Equal(result.Tree.Dump()))
		},
		Entry("an index followed by another statement",
			"CREATE NONCLUSTERED INDEX IX_Audit ON dbo.AuditRows (Id); SELECT 1;",
			"Block", "CREATE NONCLUSTERED INDEX"),
		Entry("a unique clustered index",
			"CREATE UNIQUE CLUSTERED INDEX IX_Audit ON #audit (Id);",
			"Create", "CREATE UNIQUE CLUSTERED INDEX"),
		Entry("an ALTER ADD column under IF",
			"IF 1 = 1 ALTER TABLE dbo.AuditRows ADD Ordinal VARCHAR(2) NULL; SELECT 1;",
			"Block", "ADD Ordinal VARCHAR(2) NULL"),
		Entry("a table type under IF",
			"IF TYPE_ID(N'dbo.AuditRows') IS NULL CREATE TYPE dbo.AuditRows AS TABLE (Id INT NOT NULL PRIMARY KEY);",
			"IfBlock", "AS TABLE (Id INTEGER NOT NULL PRIMARY KEY)"),
		Entry("batch RAISERROR options",
			"RAISERROR(N'audit', 0, 1) WITH NOWAIT, SETERROR; SELECT 1;",
			"Block", "WITH NOWAIT, SETERROR"),
		Entry("DELETE TOP on an aliased joined table",
			"DELETE TOP (500) a FROM dbo.AuditKeys k INNER LOOP JOIN dbo.AuditRows a ON a.Id = k.Id;",
			"Delete", "DELETE TOP (500) a FROM"),
		Entry("DELETE TOP with PERCENT and no target alias",
			"DELETE TOP (10) PERCENT FROM dbo.AuditRows;",
			"Delete", "DELETE TOP (10) PERCENT FROM"),
		Entry("comma-separated procedure names",
			"DROP PROCEDURE IF EXISTS dbo.RunAudit, dbo.StageAudit;",
			"Drop", "DROP PROCEDURE IF EXISTS dbo.RunAudit, dbo.StageAudit"),
		Entry("comma-separated function names",
			"DROP FUNCTION IF EXISTS dbo.ReadAudit, dbo.FindAudit;",
			"Drop", "DROP FUNCTION IF EXISTS dbo.ReadAudit, dbo.FindAudit"),
	)

	It("rejects TOP WITH TIES in DELETE", func() {
		result, err := ParseScript("DELETE TOP (10) WITH TIES FROM dbo.AuditRows;", "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Tree).To(BeNil())
		Expect(result.Diagnostics).To(ContainElement(HaveField("Label", ContainSubstring("WITH TIES"))))
	})
})
