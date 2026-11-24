package sqlglot

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("migration script parsing", func() {
	It("keeps valid statements after a parser diagnostic and recognizes compatibility features", func() {
		result, err := ParseMigrationScript("SELECT * FROM OPENJSON(N'{}');\nGO\nFROBNICATE items;\nGO\nSELECT STRING_AGG(name, ',') FROM items;", "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Diagnostics).To(HaveLen(1))
		Expect(result.Statements).To(HaveLen(2))
		Expect(MigrationFeatures(result.Statements[0])).To(HaveExactElements(CompatibilityFeature{Name: "OPENJSON", MinimumLevel: 130, Start: 14}))
		Expect(MigrationFeatures(result.Statements[1])).To(BeEmpty())
	})
	It("accepts comment-only migrations and rejects sqlcmd directives", func() {
		result, err := ParseMigrationScript("-- empty\nGO\n", "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Statements).To(BeEmpty())
		_, err = ParseMigrationScript(":r include.sql", "tsql")
		Expect(err).To(MatchError(ContainSubstring("sqlcmd")))
	})
	DescribeTable("records documented SQL Server compatibility gates",
		func(text, name string, level int) {
			result, err := ParseMigrationScript(text, "tsql")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Diagnostics).To(BeEmpty())
			Expect(result.Statements).To(HaveLen(1))
			feature := MigrationFeatures(result.Statements[0])
			Expect(feature).To(HaveLen(1))
			Expect(feature[0].Name).To(Equal(name))
			Expect(feature[0].MinimumLevel).To(Equal(level))
		},
		Entry("ordered STRING_AGG", "SELECT STRING_AGG(name, ',') WITHIN GROUP (ORDER BY name) FROM items;", "STRING_AGG", 110),
		Entry("STRING_SPLIT", "SELECT value FROM STRING_SPLIT('x,y', ',');", "STRING_SPLIT", 130),
		Entry("GENERATE_SERIES", "SELECT value FROM GENERATE_SERIES(1, 2);", "GENERATE_SERIES", 160),
		Entry("positional TRIM", "SELECT TRIM(LEADING 'x' FROM 'xx');", "TRIM", 160),
		Entry("extended LTRIM", "SELECT LTRIM('xy', 'x');", "LTRIM", 160),
		Entry("extended RTRIM", "SELECT RTRIM('yx', 'x');", "RTRIM", 160),
		Entry("REGEXP_LIKE", "SELECT 1 WHERE REGEXP_LIKE('x', 'x');", "REGEXP_LIKE", 170),
	)
})
