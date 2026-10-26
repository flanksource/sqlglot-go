package changeset_test

import (
	"testing"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate/changeset"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestChangeset(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Selectable change sets")
}

func id(action, kind, table, member string) string {
	return `["` + action + `","` + kind + `","app","` + table + `","` + member + `"]`
}

func requires(set *changeset.Set) map[string][]string {
	edges := map[string][]string{}
	for _, c := range set.Changes {
		edges[c.ID] = c.Requires
	}
	return edges
}

var _ = Describe("Change requirements", func() {
	var app *schema.Schema
	var dialect changeset.Dialect
	BeforeEach(func() {
		app = schema.New("app")
		dialect = changeset.Dialect{Name: "test"}
	})
	table := func(name string, columns ...string) *schema.Table {
		t := schema.NewTable(name).SetSchema(app)
		for _, c := range columns {
			t.AddColumns(schema.NewIntColumn(c, "int"))
		}
		return t
	}

	It("makes dropping a column require dropping the index and the inbound foreign key on it", func() {
		orders := table("orders", "id", "code")
		code, _ := orders.Column("code")
		index := schema.NewIndex("idx_code").AddColumns(code)
		index.Table = orders
		lines := table("lines", "order_code")
		lineCode, _ := lines.Column("order_code")
		fk := schema.NewForeignKey("fk_lines_code").SetTable(lines).AddColumns(lineCode).SetRefTable(orders).AddRefColumns(code)
		diff := []schema.Change{
			&schema.ModifyTable{T: orders, Changes: []schema.Change{&schema.DropColumn{C: code}, &schema.DropIndex{I: index}}},
			&schema.ModifyTable{T: lines, Changes: []schema.Change{&schema.DropForeignKey{F: fk}}},
		}
		set, err := changeset.Describe(diff, dialect)
		Expect(err).NotTo(HaveOccurred())
		Expect(requires(set)).To(Equal(map[string][]string{
			id("drop", "column", "orders", "code"):              {id("drop", "foreign-key", "lines", "fk_lines_code"), id("drop", "index", "orders", "idx_code")},
			id("drop", "index", "orders", "idx_code"):           nil,
			id("drop", "foreign-key", "lines", "fk_lines_code"): nil,
		}))
	})

	It("makes dropping a table require dropping the tables that still reference it", func() {
		parents := table("parents", "id")
		parentID, _ := parents.Column("id")
		children := table("children", "parent_id")
		childParent, _ := children.Column("parent_id")
		children.AddForeignKeys(schema.NewForeignKey("fk_children_parent").SetTable(children).AddColumns(childParent).SetRefTable(parents).AddRefColumns(parentID))
		set, err := changeset.Describe([]schema.Change{&schema.DropTable{T: parents}, &schema.DropTable{T: children}}, dialect)
		Expect(err).NotTo(HaveOccurred())
		Expect(requires(set)).To(Equal(map[string][]string{
			id("drop", "table", "parents", ""):  {id("drop", "table", "children", "")},
			id("drop", "table", "children", ""): nil,
		}))
	})

	It("makes a new index require its new column", func() {
		orders := table("orders", "id")
		note := schema.NewIntColumn("note", "int")
		index := schema.NewIndex("idx_note").AddColumns(note)
		set, err := changeset.Describe([]schema.Change{&schema.ModifyTable{T: orders, Changes: []schema.Change{&schema.AddColumn{C: note}, &schema.AddIndex{I: index}}}}, dialect)
		Expect(err).NotTo(HaveOccurred())
		Expect(requires(set)[id("add", "index", "orders", "idx_note")]).To(Equal([]string{id("add", "column", "orders", "note")}))
	})

	It("makes a change that rebuilds its table require every sibling, but not the other way round", func() {
		orders := table("orders", "id", "legacy")
		legacy, _ := orders.Column("legacy")
		index := schema.NewIndex("idx_id")
		dialect.Rebuilds = func(c schema.Change) bool { _, ok := c.(*schema.DropColumn); return ok }
		set, err := changeset.Describe([]schema.Change{&schema.ModifyTable{T: orders, Changes: []schema.Change{&schema.DropColumn{C: legacy}, &schema.AddIndex{I: index}}}}, dialect)
		Expect(err).NotTo(HaveOccurred())
		Expect(requires(set)).To(Equal(map[string][]string{
			id("drop", "column", "orders", "legacy"): {id("add", "index", "orders", "idx_id")},
			id("add", "index", "orders", "idx_id"):   nil,
		}))
	})

	It("keeps a table modification with only its selected members", func() {
		orders := table("orders", "id")
		a, b := schema.NewIntColumn("a", "int"), schema.NewIntColumn("b", "int")
		modify := &schema.ModifyTable{T: orders, Changes: []schema.Change{&schema.AddColumn{C: a}, &schema.AddColumn{C: b}}}
		set, err := changeset.Describe([]schema.Change{modify}, dialect)
		Expect(err).NotTo(HaveOccurred())
		kept, err := set.Select([]schema.Change{modify}, []string{id("add", "column", "orders", "b")})
		Expect(err).NotTo(HaveOccurred())
		Expect(kept).To(HaveLen(1))
		Expect(kept[0].(*schema.ModifyTable).Changes).To(Equal([]schema.Change{modify.Changes[1]}))
		Expect(modify.Changes).To(HaveLen(2))
	})
})
