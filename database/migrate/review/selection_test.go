package review

import (
	"database/sql"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func reviewChangeID(action, kind, table, member string) string {
	return `["` + action + `","` + kind + `","main","` + table + `","` + member + `"]`
}

const selectionHCL = `schema "main" {}
table "items" {
  schema = schema.main
  column "id" { type = integer }
  column "label" { type = text }
  column "note" {
    type = text
    null = true
  }
  index "idx_items_label" { columns = [column.label] }
}
table "tags" {
  schema = schema.main
  column "id" { type = integer }
}`

var (
	addTags      = reviewChangeID("add", "table", "tags", "")
	addNote      = reviewChangeID("add", "column", "items", "note")
	addLabelIdx  = reviewChangeID("add", "index", "items", "idx_items_label")
	dropOmitted  = reviewChangeID("drop", "column", "items", "omitted")
	selectionDDL = "CREATE TABLE items (id integer NOT NULL, label text NOT NULL)"
)

var _ = Describe("SQLite selective reviewed artifacts", func() {
	var db *sql.DB
	BeforeEach(func() {
		var err error
		db, err = sql.Open("sqlite", filepath.Join(GinkgoT().TempDir(), "selection.db"))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
	})
	It("describes every candidate change and plans only the selected ones", func(ctx SpecContext) {
		_, err := db.Exec(selectionDDL)
		Expect(err).NotTo(HaveOccurred())
		artifact, err := Build(ctx, db, Options{Dialect: "sqlite", Schema: "main", DesiredHCL: selectionHCL, Selection: []string{addTags}})
		Expect(err).NotTo(HaveOccurred())
		var ids []string
		for _, c := range artifact.Changes {
			ids = append(ids, c.ID)
		}
		Expect(ids).To(ConsistOf(addTags, addNote, addLabelIdx))
		Expect(artifact.Steps).To(HaveLen(1))
		Expect(artifact.Steps[0].Changes).To(Equal([]string{addTags}))
		Expect(artifact.Steps[0].SQL).To(ContainSubstring("CREATE TABLE `tags`"))
	})
	It("applies a partial artifact and recognizes it as applied while unselected changes remain", func(ctx SpecContext) {
		_, err := db.Exec(selectionDDL)
		Expect(err).NotTo(HaveOccurred())
		artifact, err := Build(ctx, db, Options{Dialect: "sqlite", Schema: "main", DesiredHCL: selectionHCL, Selection: []string{addNote, addTags}})
		Expect(err).NotTo(HaveOccurred())
		result, err := Apply(ctx, db, Request{Artifact: artifact, Hash: artifact.Hash})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Outcome).To(Equal(Applied))
		result, err = Apply(ctx, db, Request{Artifact: artifact, Hash: artifact.Hash})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Outcome).To(Equal(AlreadyApplied))

		rest, err := Build(ctx, db, Options{Dialect: "sqlite", Schema: "main", DesiredHCL: selectionHCL})
		Expect(err).NotTo(HaveOccurred())
		Expect(rest.Changes).To(HaveLen(1))
		Expect(rest.Changes[0].ID).To(Equal(addLabelIdx))
	})
	It("makes a change that rebuilds the table require every sibling change", func(ctx SpecContext) {
		_, err := db.Exec("CREATE TABLE items (id integer NOT NULL, label text NOT NULL, omitted text)")
		Expect(err).NotTo(HaveOccurred())
		desired := `schema "main" {}
table "items" {
  schema = schema.main
  column "id" { type = integer }
  column "label" { type = text }
  index "idx_items_label" { columns = [column.label] }
}`
		_, err = Build(ctx, db, Options{Dialect: "sqlite", Schema: "main", DesiredHCL: desired, Selection: []string{dropOmitted}})
		Expect(err).To(MatchError(ContainSubstring("requires unselected changes " + addLabelIdx)))

		indexOnly, err := Build(ctx, db, Options{Dialect: "sqlite", Schema: "main", DesiredHCL: desired, Selection: []string{addLabelIdx}})
		Expect(err).NotTo(HaveOccurred())
		Expect(indexOnly.Steps).To(HaveLen(1))
		Expect(indexOnly.Steps[0].SQL).To(HavePrefix("CREATE INDEX"))
	})
	It("plans from a reviewed baseline snapshot instead of the live schema", func(ctx SpecContext) {
		other, err := sql.Open("sqlite", filepath.Join(GinkgoT().TempDir(), "other.db"))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(other.Close)
		_, err = other.Exec(selectionDDL)
		Expect(err).NotTo(HaveOccurred())
		baseline, err := Inspect(ctx, other, "sqlite", "main")
		Expect(err).NotTo(HaveOccurred())
		artifact, err := Build(ctx, db, Options{Dialect: "sqlite", Schema: "main", DesiredHCL: selectionHCL, Baseline: &baseline})
		Expect(err).NotTo(HaveOccurred())
		Expect(artifact.Baseline).To(Equal(baseline))
		Expect(artifact.Changes).To(HaveLen(3))
	})
})
