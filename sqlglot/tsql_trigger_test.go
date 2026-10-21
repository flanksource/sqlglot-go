package sqlglot

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("T-SQL trigger definitions", func() {
	DescribeTable("parses and generates qualified triggers without opaque command nodes",
		func(verb string) {
			text := verb + " TRIGGER [dbo].[audit_orders] ON [dbo].[orders] AFTER INSERT, UPDATE AS BEGIN SET NOCOUNT ON; SELECT 1; END"
			parsed, err := ParseScript(text, "tsql")
			Expect(err).NotTo(HaveOccurred())
			Expect(parsed.Complete).To(BeTrue())
			Expect(parsed.Diagnostics).To(BeEmpty())
			Expect(parsed.Tree.FindAll("Command")).To(BeEmpty())
			written, err := Generate(parsed.Tree, "tsql")
			Expect(err).NotTo(HaveOccurred())
			Expect(written).To(HavePrefix(verb + " TRIGGER [dbo].[audit_orders] ON [dbo].[orders] AFTER INSERT, UPDATE AS "))
			second, err := ParseScript(written, "tsql")
			Expect(err).NotTo(HaveOccurred())
			Expect(second.Diagnostics).To(BeEmpty())
		}, Entry("create", "CREATE"), Entry("alter", "ALTER"), Entry("create or alter", "CREATE OR ALTER"))
	It("preserves execution context and replication options", func() {
		text := "CREATE TRIGGER dbo.audit_orders ON dbo.orders WITH EXECUTE AS OWNER AFTER INSERT NOT FOR REPLICATION AS SELECT 1"
		parsed, err := ParseScript(text, "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(parsed.Complete).To(BeTrue())
		written, err := Generate(parsed.Tree, "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(written).To(Equal(text))
	})
	DescribeTable("rejects invalid trigger definitions", func(text string) {
		parsed, err := ParseScript(text, "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(parsed.Tree).To(BeNil())
		Expect(parsed.Diagnostics).NotTo(BeEmpty())
	},
		Entry("missing body", "CREATE TRIGGER dbo.audit_orders ON dbo.orders AFTER INSERT AS"),
		Entry("repeated event", "CREATE TRIGGER dbo.audit_orders ON dbo.orders AFTER INSERT, INSERT AS SELECT 1"),
		Entry("invalid timing", "CREATE TRIGGER dbo.audit_orders ON dbo.orders BEFORE INSERT AS SELECT 1"),
		Entry("missing end", "CREATE TRIGGER dbo.audit_orders ON dbo.orders AFTER INSERT AS BEGIN SELECT 1"))
})
