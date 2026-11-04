package sqlglot

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("T-SQL DB_NAME references", func() {
	DescribeTable("keeps database-name calls visible to reference consumers",
		func(query string, names []string, wantSQL string) {
			tree, err := ParseOne(query, "tsql")
			Expect(err).NotTo(HaveOccurred())
			var got []string
			for _, call := range tree.FindAll("Anonymous") {
				got = append(got, call.Name())
			}
			Expect(got).To(Equal(names))
			written, err := Generate(tree, "tsql")
			Expect(err).NotTo(HaveOccurred())
			Expect(written).To(Equal(wantSQL))
		},
		Entry("current database", "SELECT DB_NAME()", []string{"DB_NAME"}, "SELECT DB_NAME()"),
		Entry("nested mixed case", "SELECT SUBSTRING(db_name(), 5, 2)", []string{"db_name"}, "SELECT SUBSTRING(DB_NAME(), 5, 2)"),
		Entry("explicit database id", "SELECT DB_NAME(1)", []string{"DB_NAME"}, "SELECT DB_NAME(1)"),
		Entry("literals and comments", "SELECT 'DB_NAME()' /* DB_NAME() */", []string(nil), "SELECT 'DB_NAME()'"),
	)
})
