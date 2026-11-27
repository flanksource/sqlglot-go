package atlasdriver

import (
	"ariga.io/atlas/schemahcl"
	atlassqlite "ariga.io/atlas/sql/sqlite"
	"github.com/hashicorp/hcl/v2/hclparse"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/zclconf/go-cty/cty"
)

// evalCountingBackend is the part of a Backend decode reaches: its name and its
// Atlas wiring, with an evaluator that counts the snapshots it evaluates.
type evalCountingBackend struct {
	Backend
	atlas Atlas
}

func (b evalCountingBackend) Name() string { return "sqlite" }
func (b evalCountingBackend) Atlas() Atlas { return b.atlas }

const cachedSnapshot = `schema "main" {}
table "parent" {
  schema = schema.main
  column "id" { type = integer }
  primary_key { columns = [column.id] }
}
table "child" {
  schema = schema.main
  column "id" { type = integer }
  column "parent_id" { type = integer }
  index "child_parent_idx" { columns = [column.parent_id] }
  foreign_key "child_parent_fk" {
    columns = [column.parent_id]
    ref_columns = [table.parent.column.id]
  }
}`

var _ = Describe("decoding a native snapshot", func() {
	It("evaluates a snapshot once and gives every caller a copy it may change", func() {
		evals := 0
		eval := schemahcl.EvalFunc(func(p *hclparse.Parser, v any, input map[string]cty.Value) error {
			evals++
			return atlassqlite.EvalHCL.Eval(p, v, input)
		})
		d := New(evalCountingBackend{atlas: Atlas{Eval: eval, Differ: atlassqlite.DefaultDiff}})

		first, err := d.decode(cachedSnapshot)
		Expect(err).NotTo(HaveOccurred())
		child, ok := first.Table("child")
		Expect(ok).To(BeTrue())
		child.Name, child.Columns = "renamed", child.Columns[:1]
		first.Tables = first.Tables[:1]

		second, err := d.decode(cachedSnapshot)
		Expect(err).NotTo(HaveOccurred())
		Expect(evals).To(Equal(1))
		pristine, err := d.evaluate(cachedSnapshot)
		Expect(err).NotTo(HaveOccurred())
		changes, err := d.diffSchemas(pristine, second)
		Expect(err).NotTo(HaveOccurred())
		Expect(changes).To(BeEmpty())

		copied, ok := second.Table("child")
		Expect(ok).To(BeTrue())
		parent, _ := second.Table("parent")
		Expect(copied.Schema).To(BeIdenticalTo(second))
		Expect(copied.Indexes[0].Parts[0].C).To(BeIdenticalTo(copied.Columns[1]))
		Expect(copied.ForeignKeys[0].RefTable).To(BeIdenticalTo(parent))
		Expect(copied.ForeignKeys[0].RefColumns[0]).To(BeIdenticalTo(parent.Columns[0]))
	})
})
