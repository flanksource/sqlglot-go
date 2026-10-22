package review

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("reviewed check expression comparison", func() {
	DescribeTable("compares parsed expression structure", func(left, right string, same bool) {
		actual, err := sameCheck(left, right, "postgres")
		Expect(err).NotTo(HaveOccurred())
		Expect(actual).To(Equal(same))
	},
		Entry("rendered outer parentheses", "id > 0", "(id > 0)", true),
		Entry("nested redundant parentheses", "((id)) > 0", "id > 0", true),
		Entry("different values", "id > 0", "id > 1", false),
		Entry("operator grouping", "(a AND b) OR c", "a AND (b OR c)", false),
		Entry("literal parentheses", "label = '(value)'", "label = 'value'", false),
	)
})
