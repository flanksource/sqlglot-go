package main

import (
	"bytes"
	"context"
	"github.com/flanksource/clicky/entity"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"time"
)

var _ = Describe("proxy commands", func() {
	It("keeps listener operations off HTTP routes", func() {
		root := newRootCommand(bytes.NewReader(nil))
		for _, name := range []string{"postgres", "tds"} {
			command, _, err := root.Find([]string{"proxy", name})
			Expect(err).NotTo(HaveOccurred())
			Expect(command.Name()).To(Equal(name))
			Expect(entity.IsLocalOnly(command)).To(BeTrue())
		}
	})
	It("validates the upstream before starting a listener", func() {
		root := newRootCommand(bytes.NewReader(nil))
		root.SetArgs([]string{"proxy", "postgres"})
		Expect(root.ExecuteContext(context.Background())).To(MatchError(ContainSubstring("upstream")))
	})
	It("reports the real address on stderr and exits on cancellation", func() {
		var output, diagnostics bytes.Buffer
		root := newRootCommand(bytes.NewReader(nil))
		root.SetOut(&output)
		root.SetErr(&diagnostics)
		root.SetArgs([]string{"proxy", "postgres", "--upstream", "127.0.0.1:5432", "--json"})
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		Expect(root.ExecuteContext(ctx)).To(Succeed())
		Expect(output.String()).To(BeEmpty())
		Expect(diagnostics.String()).To(ContainSubstring("proxy listening on 127.0.0.1:"))
		Expect(diagnostics.String()).NotTo(ContainSubstring("127.0.0.1:0"))
	})
})
