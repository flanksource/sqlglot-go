package workflow

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("TransformSource", func() {
	const filename = "procs/orders.xml"
	bodies := map[string]string{"procs/orders.sql": "select * from orders"}
	read := func(path string) (string, error) {
		body, ok := bodies[path]
		if !ok {
			return "", fmt.Errorf("no source %q", path)
		}
		return body, nil
	}

	It("exposes file, captures and the caller's read to the expression", func() {
		fields, err := TransformSource(
			`{"sql": read(captures.name + ".sql"), "id": file.path, "label": captures.name}`,
			filename, "<proc/>", map[string]string{"name": "procs/orders"}, read)

		Expect(err).NotTo(HaveOccurred())
		Expect(fields).To(Equal(map[string]any{
			"sql": "select * from orders", "id": filename, "label": "procs/orders",
		}))
	})

	DescribeTable("rejects", func(expression, message string) {
		_, err := TransformSource(expression, filename, "<proc/>", nil, read)
		Expect(err).To(MatchError(ContainSubstring(message)))
	},
		Entry("a non-map result", `file.content`, "transform must return a map"),
		Entry("a failing read", `{"sql": read("missing.sql")}`, `no source "missing.sql"`),
	)
})
