package sqlserver_test

import (
	"ariga.io/atlas/sql/schema"
	planner "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL Server module type semantics", func() {
	DescribeTable("does not erase semantically different parameter types", func(before, after string) {
		from := routineSchema("CREATE PROCEDURE dbo.acme_types @value " + before + " AS SELECT @value")
		to := routineSchema("CREATE PROCEDURE dbo.acme_types @value " + after + " AS SELECT @value")
		changes, err := planner.DefaultDiff.SchemaDiff(from, to)
		Expect(err).NotTo(HaveOccurred())
		Expect(changes).To(ConsistOf(BeAssignableToTypeOf(&schema.ModifyProc{})))
	}, Entry("real and float", "REAL", "FLOAT"), Entry("ntext and text", "NTEXT", "TEXT"))
})
