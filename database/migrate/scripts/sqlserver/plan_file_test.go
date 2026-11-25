package sqlserver

import (
	"encoding/json"

	"github.com/flanksource/sqlglot-go/database/migrate/scripts"
	"github.com/flanksource/sqlglot-go/migrate/engine"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Plan file versions", func() {
	sealed := func(version int) PlanFile {
		GinkgoHelper()
		file, err := PlanFile{Kind: PlanFileKind, Version: version, Target: PlanTarget{Server: "srv", Database: "db", Scope: "acme"},
			Ledger: map[string]scripts.State{}, Observations: map[string]string{}}.seal()
		Expect(err).NotTo(HaveOccurred())
		return file
	}
	roundTrip := func(file PlanFile) (PlanFile, error) {
		encoded, err := json.Marshal(file)
		Expect(err).NotTo(HaveOccurred())
		var decoded PlanFile
		return decoded, json.Unmarshal(encoded, &decoded)
	}

	It("writes the current version", func() {
		Expect(PlanFileVersion).To(Equal(3))
	})

	It("reads a sealed version 2 file that has no schema step", func() {
		decoded, err := roundTrip(sealed(2))
		Expect(err).NotTo(HaveOccurred())
		Expect(decoded.Verify()).To(Succeed())
	})

	It("reads a sealed version 3 file", func() {
		decoded, err := roundTrip(sealed(3))
		Expect(err).NotTo(HaveOccurred())
		Expect(decoded.Verify()).To(Succeed())
	})

	It("refuses a version 2 file that carries schema statements, naming the older version and the re-plan", func() {
		legacy := `{"kind":"sqlglot-go/script-plan","version":2,"target":{"server":"s","database":"d","scope":"x"},` +
			`"bundle":{"scripts":[],"schema":{"objects":["dbo.acme"],"statements":[{"sql":"ALTER TABLE dbo.acme ADD n int"}]}},` +
			`"ledger":{},"observations":{},"actions":[],"hash":"h"}`
		var decoded PlanFile
		Expect(json.Unmarshal([]byte(legacy), &decoded)).To(MatchError(And(ContainSubstring("older version"), ContainSubstring("re-planned"))))
	})

	It("refuses a version 2 value that carries a schema artifact", func() {
		file := sealed(2)
		file.Bundle.Schema = &engine.Artifact{}
		Expect(file.Verify()).To(MatchError(ContainSubstring("older version")))
	})

	DescribeTable("refuses versions it does not read", func(version int, message string) {
		Expect(sealed(version).Verify()).To(MatchError(ContainSubstring(message)))
	},
		Entry("version 1", 1, "re-run sqlglot migrate plan"),
		Entry("a future version", 4, "not a version 2 or 3"),
	)

	It("refuses a plan file with a field it does not know", func() {
		var decoded PlanFile
		Expect(json.Unmarshal([]byte(`{"kind":"sqlglot-go/script-plan","version":3,"extra":1}`), &decoded)).To(MatchError(ContainSubstring("unknown field")))
	})
})
