package sqlserver_test

import (
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL Server member dependency order", func() {
	It("drops removed indexes before altering their column", func() {
		current := migrate.Table{Name: migrate.Name{Schema: "dbo", Name: "orders"}, Columns: []migrate.Column{{Name: "label", Type: "nvarchar(20)"}}, Indexes: []migrate.Index{{Name: "IX_label", Columns: []migrate.IndexColumn{{Name: "label"}}}}}
		desired := migrate.Table{Name: current.Name, Columns: []migrate.Column{{Name: "label", Type: "int"}}}
		plan, err := sqlserver.Plan(migrate.Request{Current: migrate.Schema{Tables: []migrate.Table{current}}, Desired: migrate.Schema{Tables: []migrate.Table{desired}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Steps[0].SQL).To(Equal("DROP INDEX [IX_label] ON [dbo].[orders]"))
		Expect(plan.Steps[1].SQL).To(Equal("ALTER TABLE [dbo].[orders] ALTER COLUMN [label] int NOT NULL"))
	})

	It("splits index replacements around column changes while retaining their original review identity", func() {
		current := migrate.Table{Name: migrate.Name{Schema: "dbo", Name: "orders"}, Columns: []migrate.Column{{Name: "label", Type: "nvarchar(20)"}}, Indexes: []migrate.Index{{Name: "IX_label", Columns: []migrate.IndexColumn{{Name: "label"}}}}}
		desired := migrate.Table{Name: current.Name, Columns: []migrate.Column{{Name: "label", Type: "int"}}, Indexes: []migrate.Index{{Name: "IX_label", Unique: true, Columns: []migrate.IndexColumn{{Name: "label"}}}}}
		plan, err := sqlserver.Plan(migrate.Request{Current: migrate.Schema{Tables: []migrate.Table{current}}, Desired: migrate.Schema{Tables: []migrate.Table{desired}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Steps).To(HaveLen(3))
		Expect(plan.Steps[0].SQL).To(HavePrefix("DROP INDEX"))
		Expect(plan.Steps[1].SQL).To(ContainSubstring("ALTER COLUMN"))
		Expect(plan.Steps[2].SQL).To(HavePrefix("CREATE UNIQUE"))
		Expect(plan.Steps[0].Change).To(Equal(plan.Steps[2].Change))
		Expect(plan.Steps[0].Change.Action).To(Equal("modify"))
		Expect(plan.Changes).To(HaveLen(2))
	})
})
