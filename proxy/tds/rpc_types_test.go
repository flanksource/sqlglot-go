package tds

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Encodings no captured driver sends; the captured ones are asserted in fixtures_test.go.
var _ = Describe("RPC scalar parameters", func() {
	DescribeTable("decodes", func(body []byte, expected any) {
		value, used, err := parameter(append(body, 0xee))
		Expect(err).NotTo(HaveOccurred())
		Expect(value).To(Equal(expected))
		Expect(used).To(Equal(len(body)))
	},
		Entry("smallmoney", []byte{0x6e, 4, 4, 0x18, 0xfc, 0xff, 0xff}, "-0.1000"),
		Entry("money beyond 32 bits", []byte{0x6e, 8, 8, 1, 0, 0, 0, 0, 0, 0, 0}, "429496.7296"),
		Entry("smalldatetime", []byte{0x6f, 4, 4, 1, 0, 90, 0}, "1900-01-02T01:30:00"),
		Entry("datetime before 1900", []byte{0x6f, 8, 8, 0xff, 0xff, 0xff, 0xff, 0x2c, 1, 0, 0}, "1899-12-31T00:00:01.000"),
		Entry("decimal smaller than its scale", []byte{0x6a, 5, 5, 3, 5, 1, 7, 0, 0, 0}, "0.007"),
		Entry("numeric without scale", []byte{0x6c, 5, 5, 0, 5, 0, 7, 0, 0, 0}, "-7"),
		Entry("time without fraction", []byte{0x29, 0, 3, 0x0a, 0, 0}, "00:00:10"),
		Entry("datetimeoffset crossing midnight", []byte{0x2b, 0, 8, 0, 0, 0, 1, 0, 0, 0xc4, 0xff}, "0001-01-01T23:00:00-01:00"),
	)
	It("consumes only the type byte of NULLTYPE", func() {
		value, used, err := parameter([]byte{0x1f, 0xee})
		Expect(err).NotTo(HaveOccurred())
		Expect(value).To(BeNil())
		Expect(used).To(Equal(1))
	})
	DescribeTable("rejects", func(body []byte, expected string) {
		_, _, err := parameter(body)
		Expect(err).To(MatchError(ContainSubstring(expected)))
	},
		Entry("an empty parameter", []byte{}, "truncated RPC type"),
		Entry("a missing length", []byte{0x6a, 5, 5, 3}, "truncated RPC parameter type"),
		Entry("a short value", []byte{0x24, 16, 16, 1, 2}, "truncated RPC parameter value"),
		Entry("an impossible length", []byte{0x24, 16, 3, 1, 2, 3}, "invalid RPC parameter length"),
		Entry("an unsupported type", []byte{0xf1, 0}, "unsupported RPC parameter type 0xf1"),
	)
})
