package atlasdriver_test

import (
	"slices"
	"testing"

	"github.com/flanksource/sqlglot-go/database/capabilities"
	"github.com/flanksource/sqlglot-go/database/migrate/dialect/atlasdriver"
	"github.com/flanksource/sqlglot-go/database/migrate/dialect/sqlite"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestAtlasDriver(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Atlas driver")
}

const parentChild = `schema "main" {}
table "parent" {
  schema = schema.main
  column "id" { type = integer }
}
table "child" {
  schema = schema.main
  column "id" { type = integer }
  column "parent_id" { type = integer }
  foreign_key "child_parent_fk" {
    columns = [column.parent_id]
    ref_columns = [table.parent.column.id]
  }
}`

// narrowed keeps the tables named in objects, the way the engine narrows a schema to a unit.
func narrowed(s migrate.Schema, objects ...string) migrate.Schema {
	s.Tables = slices.DeleteFunc(slices.Clone(s.Tables), func(t migrate.Table) bool { return !slices.Contains(objects, t.Name.Name) })
	return s
}

var _ = Describe("narrowing a native snapshot to its projection", func() {
	var desired migrate.Schema
	BeforeEach(func() {
		var err error
		desired, err = sqlite.SchemaFromHCL(parentChild)
		Expect(err).NotTo(HaveOccurred())
	})

	It("reports nothing for a foreign key whose target is outside the narrowed set", func() {
		diff, err := sqlite.Dialect.Diff(narrowed(desired, "child"), narrowed(desired, "child"))
		Expect(err).NotTo(HaveOccurred())
		Expect(diff.Changes()).To(BeEmpty())
	})

	It("still reports the changes of the narrowed tables", func() {
		extra, err := sqlite.SchemaFromHCL(`schema "main" {}
table "parent" {
  schema = schema.main
  column "id" { type = integer }
}
table "child" {
  schema = schema.main
  column "id" { type = integer }
  column "parent_id" { type = integer }
  column "note" {
    type = text
    null = true
  }
  foreign_key "child_parent_fk" {
    columns = [column.parent_id]
    ref_columns = [table.parent.column.id]
  }
}`)
		Expect(err).NotTo(HaveOccurred())
		diff, err := sqlite.Dialect.Diff(narrowed(desired, "child"), narrowed(extra, "child"))
		Expect(err).NotTo(HaveOccurred())
		ids := []string{}
		for _, c := range diff.Changes() {
			ids = append(ids, c.ID)
		}
		Expect(ids).To(Equal([]string{`["add","column","main","child","note"]`}))
	})

	It("fails when the projection names a table the native snapshot lacks", func() {
		ghost := desired
		ghost.Tables = append(slices.Clone(ghost.Tables), migrate.Table{Name: migrate.Name{Schema: "main", Name: "ghost"}})
		_, err := sqlite.Dialect.Diff(ghost, desired)
		Expect(err).To(MatchError(ContainSubstring("ghost")))
	})

	It("fails loudly on a schema with no native snapshot", func() {
		_, err := sqlite.Dialect.Diff(migrate.Schema{}, desired)
		Expect(err).To(MatchError(ContainSubstring("no Native snapshot")))
	})
})

var _ = Describe("capability mapping", func() {
	probed := func(transactional bool) capabilities.BackendCapabilities {
		return capabilities.BackendCapabilities{
			Backend: "sqlite", Version: "3.45.0", Database: "main",
			Features: map[capabilities.BackendCapability]capabilities.BackendCapabilityStatus{
				capabilities.BackendCapabilityTransactionalDDL:     {Supported: transactional},
				capabilities.BackendCapabilityTableRebuildRequired: {Supported: true},
				capabilities.BackendCapabilityOnlineIndex:          {},
			},
		}
	}

	It("maps every reported capability to a feature keyed by its constant and carries the fingerprint", func() {
		in := probed(true)
		want, err := in.Fingerprint()
		Expect(err).NotTo(HaveOccurred())
		got, err := atlasdriver.MapCapabilities(in)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(engine.Capabilities{
			Fingerprint: want, TransactionalDDL: true,
			Features: map[string]bool{"transactionalDDL": true, "tableRebuildRequired": true, "onlineIndex": false},
		}))
	})

	It("reports non-transactional DDL, and refuses a probe that omits the capability", func() {
		got, err := atlasdriver.MapCapabilities(probed(false))
		Expect(err).NotTo(HaveOccurred())
		Expect(got.TransactionalDDL).To(BeFalse())

		missing := probed(true)
		delete(missing.Features, capabilities.BackendCapabilityTransactionalDDL)
		_, err = atlasdriver.MapCapabilities(missing)
		Expect(err).To(MatchError(ContainSubstring("transactionalDDL")))
	})
})
