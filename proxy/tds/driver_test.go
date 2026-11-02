package tds

import (
	"database/sql"
	"fmt"
	"github.com/flanksource/sqlglot-go/proxy"
	"github.com/flanksource/sqlglot-go/proxy/internal/testcert"
	_ "github.com/microsoft/go-mssqldb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"net"
	"net/url"
)

var _ = Describe("SQL Server driver interoperability", func() {
	for _, encrypted := range []bool{false, true} {
		It(fmt.Sprintf("relays go-mssqldb login and batches with client TLS=%t", encrypted), func(ctx SpecContext) {
			cert, _, err := testcert.Configs()
			Expect(err).NotTo(HaveOccurred())
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
				if _, err = readMessage(conn); err != nil {
					done <- err
					return
				}
				if _, err = conn.Write(buildTDSPacket(typeTabularResult, statusEOM, prelogin(2))); err != nil {
					done <- err
					return
				}
				if _, err = readMessage(conn); err != nil {
					done <- err
					return
				}
				ack := []byte{tokenLoginAck, 10, 0, 1, 0x74, 0, 0, 4, 0, 15, 0, 0, 0}
				completion := make([]byte, 13)
				completion[0] = tokenDone
				if _, err = conn.Write(buildTDSPacket(typeTabularResult, statusEOM, append(ack, completion...))); err != nil {
					done <- err
					return
				}
				if _, err = readMessage(conn); err != nil {
					done <- err
					return
				}
				_, err = conn.Write(buildTDSPacket(typeTabularResult, statusEOM, completion))
				done <- err
			}()
			opts := proxy.Options{Upstream: listener.Addr().String()}
			if encrypted {
				opts.ClientTLSMode = "allow"
				opts.ClientTLS = cert
			}
			server, err := Start(ctx, opts)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(server.Close)
			query := url.Values{"encrypt": {"disable"}}
			if encrypted {
				query.Set("encrypt", "true")
				query.Set("trustservercertificate", "true")
			}
			db, err := sql.Open("sqlserver", "sqlserver://acme:example@"+server.Addr()+"?"+query.Encode())
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(db.Close)
			_, err = db.ExecContext(ctx, "SELECT 1")
			Expect(err).NotTo(HaveOccurred())
			Expect(<-done).To(Succeed())
		})
	}
})
