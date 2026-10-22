package migrate

import (
	"ariga.io/atlas/sql/schema"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Shared migration drop filtering", func() {
	It("skips column and table drops while retaining other changes without mutating the input", func() {
		columnDrop := &schema.DropColumn{C: schema.NewStringColumn("note", "text")}
		indexDrop := &schema.DropIndex{I: schema.NewIndex("old_index")}
		mixed := &schema.ModifyTable{T: schema.NewTable("events"), Changes: []schema.Change{columnDrop, indexDrop}}
		onlyDrop := &schema.ModifyTable{T: schema.NewTable("other"), Changes: []schema.Change{columnDrop}}
		filtered := withoutDrops([]schema.Change{mixed, onlyDrop, &schema.DropTable{T: schema.NewTable("omitted")}})
		Expect(filtered).To(HaveLen(1))
		Expect(filtered[0]).To(Equal(&schema.ModifyTable{T: mixed.T, Changes: []schema.Change{indexDrop}}))
		Expect(mixed.Changes).To(Equal([]schema.Change{columnDrop, indexDrop}))
	})
})
