package sqlserver

import (
	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/migrate"
	atlas "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Atlas snapshot projection", func() {
	It("retains identity, defaults, clustered keys, constraints and routines", func() {
		input := migrate.Schema{Tables: []migrate.Table{{Name: migrate.Name{Schema: "dbo", Name: "items"}, Columns: []migrate.Column{
			{Name: "id", Type: "int", Identity: &migrate.Identity{Seed: 1, Increment: 1}},
			{Name: "label", Type: "nvarchar(20)", Nullable: true, Default: &migrate.Default{Name: "DF_items_label", Expression: "N'new'"}},
		}, Indexes: []migrate.Index{{Name: "PK_items", Primary: true, Unique: true, Clustered: true, Columns: []migrate.IndexColumn{{Name: "id"}}}}, Checks: []migrate.Check{{Name: "positive_id", Expression: "id > 0"}}}}}
		r, err := realm(input, []string{"dbo"})
		Expect(err).NotTo(HaveOccurred())
		proc, err := atlas.ParseRoutine("CREATE PROCEDURE dbo.example AS SELECT 1")
		Expect(err).NotTo(HaveOccurred())
		Expect(proc.AddTo(r.Schemas[0])).To(Succeed())
		output, err := FromAtlas(r)
		Expect(err).NotTo(HaveOccurred())
		input.Definitions = []migrate.Definition{{Name: migrate.Name{Schema: "dbo", Name: "example"}, Kind: "PROCEDURE", SQL: proc.Text}}
		Expect(output).To(Equal(input))
	})
	It("rejects unsupported realm features", func() {
		_, err := FromAtlas(schema.NewRealm(schema.New("dbo").AddViews(schema.NewView("v", "SELECT 1"))))
		Expect(err).To(HaveOccurred())
	})
})
