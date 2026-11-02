package main

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"testing"
)

func TestClient(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Fixture client") }

var _ = Describe("Fixture client boundaries", func() {
	It("rejects an unset DSN", func() {
		_, err := query(options{dsnEnv: "UNSET_PROXY_FIXTURE_DSN"})
		Expect(err).To(MatchError("fixture DSN is unset"))
	})
	It("rejects an unknown operation", func() { _, err := run(options{mode: "invalid"}); Expect(err).To(MatchError("unknown mode")) })
})
