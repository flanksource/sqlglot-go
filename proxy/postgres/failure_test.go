package postgres

import (
	"crypto/x509"
	"github.com/flanksource/sqlglot-go/proxy"
	"github.com/flanksource/sqlglot-go/proxy/internal/testcert"
	"github.com/jackc/pgx/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"net"
)

var _ = Describe("TLS failures", func() {
	DescribeTable("does not retry upstream TLS as plaintext", func(ctx SpecContext, mode, expected string) {
		cert, trust, err := testcert.Configs()
		Expect(err).NotTo(HaveOccurred())
		if mode == "hostname" {
			trust.ServerName = "wrong.example"
		} else {
			trust.RootCAs = x509.NewCertPool()
		}
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(listener.Close)
		done := make(chan error, 1)
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			defer func() { _ = conn.Close() }()
			done <- fakePostgres(ctx, conn, cert, true)
		}()
		rows := make(chan proxy.Statement, 4)
		server, err := Start(ctx, proxy.Options{Upstream: listener.Addr().String(), UpstreamTLS: trust, OnStatement: func(row proxy.Statement) error { rows <- row; return nil }})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(server.Close)
		_, err = pgx.Connect(ctx, "postgres://acme@"+server.Addr()+"/example?sslmode=disable")
		Expect(err).To(HaveOccurred())
		var row proxy.Statement
		Eventually(rows).Should(Receive(&row))
		Expect(row.Error).To(ContainSubstring(expected))
		Expect(row.Incomplete).To(BeTrue())
		Expect(<-done).To(HaveOccurred())
	}, Entry("hostname mismatch", "hostname", "wrong.example"), Entry("untrusted CA", "ca", "unknown authority"))
	It("rejects channel-bound SCRAM without recording authentication data", func() {
		var rows []proxy.Statement
		session := newPGSession("example", false, func(row proxy.Statement) { rows = append(rows, row) })
		sniff := observe(session, true)
		_, err := sniff.Write(wireMessage('p', "SCRAM-SHA-256-PLUS\x00credential"))
		Expect(err).To(MatchError(ContainSubstring("channel binding")))
		Expect(rows).To(BeEmpty())
	})
	It("rejects invalid TLS configuration before listening", func(ctx SpecContext) {
		_, err := Start(ctx, proxy.Options{Upstream: "localhost:5432", ClientTLSMode: "invalid"})
		Expect(err).To(MatchError(ContainSubstring("client TLS mode")))
	})
})
