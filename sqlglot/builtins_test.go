package sqlglot

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("state-dependent builtins", func() {
	DescribeTable("maps the first five dependency groups in parsed T-SQL",
		func(dependency BuiltinDependency, expressions []string) {
			for _, expression := range expressions {
				name := strings.SplitN(expression, "(", 2)[0]
				want := BuiltinFunction{Name: name, Dependency: dependency}
				builtin, ok := LookupBuiltin(strings.ToLower(name), "sqlserver")
				Expect(ok).To(BeTrue(), expression)
				Expect(builtin).To(Equal(want), expression)
				tree, err := ParseOne("SELECT "+strings.ToLower(expression), "tsql")
				Expect(err).NotTo(HaveOccurred(), expression)
				var found []BuiltinFunction
				tree.Walk(func(node *Expression) bool {
					if ref, ok := node.Builtin("tsql"); ok {
						found = append(found, ref)
					}
					return true
				})
				Expect(found).To(Equal([]BuiltinFunction{want}), expression)
			}
		},
		Entry("environment", DependencyEnvironment, []string{"DB_NAME()", "DB_ID()", "SERVERPROPERTY('Edition')", "DATABASEPROPERTYEX('example', 'Status')", "@@SERVERNAME"}),
		Entry("security", DependencySecurity, []string{"CURRENT_USER", "SESSION_USER", "SYSTEM_USER", "USER_NAME()", "SUSER_SNAME()", "ORIGINAL_LOGIN()", "IS_MEMBER('public')"}),
		Entry("session", DependencySession, []string{"APP_NAME()", "HOST_NAME()", "CONNECTIONPROPERTY('net_transport')", "CONTEXT_INFO()", "SESSION_CONTEXT(N'key')"}),
		Entry("time", DependencyTime, []string{"GETDATE()", "GETUTCDATE()", "SYSDATETIME()", "SYSUTCDATETIME()", "SYSDATETIMEOFFSET()", "CURRENT_TIMESTAMP"}),
		Entry("randomness", DependencyRandomness, []string{"NEWID()", "NEWSEQUENTIALID()", "RAND()", "RAND(7)"}),
	)

	DescribeTable("does not map excluded groups, pure calls, identifiers or user functions",
		func(query string) {
			tree, err := ParseOne(query, "tsql")
			Expect(err).NotTo(HaveOccurred())
			tree.Walk(func(node *Expression) bool {
				_, ok := node.Builtin("tsql")
				Expect(ok).To(BeFalse(), "%s: %s", query, node.Class)
				return true
			})
		},
		Entry("execution state", "SELECT @@ROWCOUNT, @@IDENTITY, @@ERROR, @@TRANCOUNT, ERROR_NUMBER(), XACT_STATE()"),
		Entry("metadata", "SELECT OBJECT_ID('example'), OBJECT_NAME(1), SCHEMA_NAME(), OBJECT_DEFINITION(1)"),
		Entry("locale", "SELECT FORMAT(1, 'N'), DATENAME(month, '2026-01-01'), DATEPART(weekday, '2026-01-01')"),
		Entry("pure functions", "SELECT ABS(-1), ISNULL(NULL, 1), ISNUMERIC('1'), SUBSTRING('abc', 1, 1)"),
		Entry("identifiers and literals", "SELECT [GETDATE], [DB_NAME], '@@SERVERNAME', 'NEWID()' /* RAND() */ FROM [DB_NAME]"),
		Entry("qualified user functions", "SELECT dbo.DB_NAME(), dbo.GETDATE(), audit.NEWID(), dbo.RAND()"),
		Entry("single-at parameter", "SELECT @SERVERNAME"),
		Entry("sequence mutation", "SELECT NEXT VALUE FOR example_sequence"),
	)

	It("keeps the function's original identity when an expression is copied", func() {
		tree, err := ParseOne("SELECT GETDATE(), SYSDATETIME(), SYSTEM_USER, SUSER_SNAME()", "tsql")
		Expect(err).NotTo(HaveOccurred())
		var found []string
		tree.Copy().Walk(func(node *Expression) bool {
			if builtin, ok := node.Builtin("tsql"); ok {
				found = append(found, builtin.Name)
			}
			return true
		})
		Expect(found).To(Equal([]string{"GETDATE", "SYSDATETIME", "SYSTEM_USER", "SUSER_SNAME"}))
	})

	It("does not apply the T-SQL catalog to another dialect", func() {
		_, ok := LookupBuiltin("DB_NAME", "postgres")
		Expect(ok).To(BeFalse())
	})
})
