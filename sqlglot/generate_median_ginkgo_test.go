package sqlglot

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The reference writes a Median as PERCENTILE_CONT at the 0.5 level wherever
// its generator sets SUPPORTS_MEDIAN false, and as the plain call elsewhere.
// Each expectation is the reference's own output at the pinned commit.
var _ = Describe("Median generation", func() {
	DescribeTable("writes MEDIAN(x) the way the reference does",
		func(dialect, expected string) {
			tree, err := ParseOne("SELECT MEDIAN(x) FROM t", "duckdb")
			Expect(err).NotTo(HaveOccurred())
			Expect(Generate(tree, dialect)).To(Equal(expected))
		},
		Entry("sqlite has no MEDIAN", "sqlite", "SELECT PERCENTILE_CONT(x, 0.5) FROM t"),
		Entry("mysql has no MEDIAN", "mysql", "SELECT PERCENTILE_CONT(x, 0.5) FROM t"),
		Entry("presto has no MEDIAN", "presto", "SELECT PERCENTILE_CONT(x, 0.5) FROM t"),
		Entry("duckdb writes MEDIAN", "duckdb", "SELECT MEDIAN(x) FROM t"),
		Entry("tsql writes MEDIAN", "tsql", "SELECT MEDIAN(x) FROM t"),
		Entry("databricks writes MEDIAN", "databricks", "SELECT MEDIAN(x) FROM t"),
	)

	It("reads its own sqlite rewrite back as the same percentile", func() {
		tree, err := ParseOne("SELECT median(bytes) AS m FROM events GROUP BY service", "sqlite")
		Expect(err).NotTo(HaveOccurred())
		written, err := Generate(tree, "sqlite")
		Expect(err).NotTo(HaveOccurred())
		Expect(written).To(Equal("SELECT PERCENTILE_CONT(bytes, 0.5) AS m FROM events GROUP BY service"))
		reread, err := ParseOne(written, "sqlite")
		Expect(err).NotTo(HaveOccurred())
		Expect(Generate(reread, "sqlite")).To(Equal(written))
	})
})
