package sqlglot

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("MAP generation", func() {
	DescribeTable("rejects mismatched key and value arrays without panicking", func(sql string, removeKey bool) {
		tree, err := ParseOne(sql, "databricks")
		Expect(err).NotTo(HaveOccurred())
		arg := "values"
		if removeKey {
			arg = "keys"
		}
		array := tree.Args[arg].(*Expression)
		array.Set("expressions", []*Expression{})
		Expect(func() {
			_, err = Generate(tree, "databricks")
		}).NotTo(Panic())
		Expect(err).To(MatchError(ContainSubstring("mismatched key and value counts")))
	},
		Entry("missing values", "MAP(1, 'one')", false),
		Entry("extra values", "MAP(1, 'one')", true),
	)
	It("interleaves key and value pairs", func() {
		tree, err := ParseOne("MAP(1, 'one', 2, 'two')", "databricks")
		Expect(err).NotTo(HaveOccurred())
		written, err := Generate(tree, "databricks")
		Expect(err).NotTo(HaveOccurred())
		Expect(written).To(Equal("MAP(1, 'one', 2, 'two')"))
	})
})
