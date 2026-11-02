package proxy_test

import (
	"context"
	"errors"
	"github.com/flanksource/sqlglot-go/proxy"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"net"
	"testing"
)

func TestProxy(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Proxy") }

var _ = Describe("Proxy configuration", func() {
	It("rejects missing upstream before binding", func() {
		_, err := proxy.Start(context.Background(), proxy.Options{}, nil)
		Expect(err).To(MatchError(ContainSubstring("upstream")))
	})
	It("rejects TLS allow without a certificate", func() {
		_, err := proxy.Start(context.Background(), proxy.Options{Upstream: "localhost:5432", ClientTLSMode: "allow"}, nil)
		Expect(err).To(MatchError(ContainSubstring("certificate")))
	})
	It("stops and reports callback failure without hanging active connections", func(ctx SpecContext) {
		failure := errors.New("capture output failed")
		server, err := proxy.Start(ctx, proxy.Options{Upstream: "localhost:5432", OnStatement: func(proxy.Statement) error { return failure }}, func(_ context.Context, s *proxy.Server, _ net.Conn) error { s.Emit(proxy.Statement{}); return nil })
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = server.Close() })
		client, err := net.Dial("tcp", server.Addr())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(client.Close)
		done := make(chan error, 1)
		go func() { done <- server.Wait() }()
		Eventually(done).Should(Receive(MatchError(failure)))
		Expect(server.Close()).To(MatchError(failure))
		_, err = net.Dial("tcp", server.Addr())
		Expect(err).To(HaveOccurred())
	})
})
