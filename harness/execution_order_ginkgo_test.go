package harness

import (
	"testing"

	"github.com/flanksource/sqlglot-go/sqlglot"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestExecutionOrder(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Execution order metadata")
}

var _ = DescribeTable("Execution order metadata", func(sql string, ordered bool) {
	tree, err := sqlglot.ParseOne(sql, "duckdb")
	Expect(err).NotTo(HaveOccurred())
	Expect(executionOrdered(tree)).To(Equal(ordered))
},
	Entry("unordered select", "SELECT a FROM t", false),
	Entry("top-level order", "SELECT a FROM t ORDER BY a", true),
	Entry("window order", "SELECT ROW_NUMBER() OVER (ORDER BY a) FROM t", false),
	Entry("inner query order", "SELECT * FROM (SELECT a FROM t ORDER BY a) AS sub", false),
	Entry("quoted order text", "SELECT 'ORDER BY a' FROM t", false),
	Entry("set operation order", "SELECT a FROM t UNION SELECT a FROM x ORDER BY a", true),
	Entry("parenthesized ordered query", "(SELECT a FROM t ORDER BY a)", true),
)
