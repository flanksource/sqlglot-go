package sqlinspect

import (
	connection "github.com/flanksource/sqlglot-go/database/capabilities"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL catalog capabilities", func() {
	It("carries the live backend metadata on the catalog", func() {
		capabilities := connection.BackendCapabilities{
			Backend: "postgres", Version: "17.4", Database: "warehouse",
			Features: map[connection.BackendCapability]connection.BackendCapabilityStatus{
				connection.BackendCapabilityArrayFilters: {Supported: true},
			},
		}
		catalog := buildCatalog(catalogInput{Driver: "postgres", Database: "warehouse", DefaultSchema: "public", Databases: []string{"warehouse"}, Schemas: []string{"public"}, Capabilities: capabilities})

		Expect(catalog.Capabilities).To(Equal(capabilities))
	})
})
