package sqlserver

import (
	"github.com/flanksource/sqlglot-go/migrate"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("changeGuard", func() {
	It("refuses a change it cannot guard, naming the change", func() {
		change := migrate.Change{ID: `["modify","attribute","dbo","orders","comment"]`, Action: "modify", Kind: "attribute", Object: migrate.Name{Schema: "dbo", Name: "orders"}, Member: "comment"}
		_, err := changeGuard(snapshots{}, change)
		Expect(err).To(MatchError(ContainSubstring(change.ID)))
	})
})
