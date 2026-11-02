package postgres

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"github.com/flanksource/sqlglot-go/proxy"
	"github.com/flanksource/sqlglot-go/proxy/internal/testcert"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgproto3"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"io"
	"net"
	"sync"
)

var _ = Describe("PostgreSQL transport", func() {
	for _, clientTLS := range []bool{false, true} {
		for _, upstreamTLS := range []bool{false, true} {
			It("relays queries with independent TLS settings "+map[bool]string{true: "TLS", false: "plain"}[clientTLS]+"/"+map[bool]string{true: "TLS", false: "plain"}[upstreamTLS], func(ctx SpecContext) {
				cert, trust, err := testcert.Configs()
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
					done <- fakePostgres(ctx, conn, cert, upstreamTLS)
				}()
				var mu sync.Mutex
				var rows []proxy.Statement
				opts := proxy.Options{Upstream: listener.Addr().String(), OnStatement: func(row proxy.Statement) error { mu.Lock(); defer mu.Unlock(); rows = append(rows, row); return nil }}
				if clientTLS {
					opts.ClientTLSMode = "allow"
					opts.ClientTLS = cert
				}
				if upstreamTLS {
					opts.UpstreamTLS = trust
				}
				server, err := Start(ctx, opts)
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(server.Close)
				config, err := pgx.ParseConfig("postgres://acme@" + server.Addr() + "/example?sslmode=disable&channel_binding=disable")
				Expect(err).NotTo(HaveOccurred())
				if clientTLS {
					config.TLSConfig = trust
				}
				conn, err := pgx.ConnectConfig(ctx, config)
				Expect(err).NotTo(HaveOccurred())
				result, err := conn.PgConn().Exec(ctx, "SELECT 1").ReadAll()
				Expect(err).NotTo(HaveOccurred())
				Expect(result[0].CommandTag.String()).To(Equal("SELECT 1"))
				Expect(conn.Close(ctx)).To(Succeed())
				Expect(<-done).To(Succeed())
				Expect(server.Close()).To(Succeed())
				mu.Lock()
				defer mu.Unlock()
				Expect(rows).To(HaveLen(1))
				Expect(rows[0].SQL).To(Equal("SELECT 1"))
				Expect(rows[0].Rows).To(Equal(int64(1)))
			})
		}
	}
})

func fakePostgres(ctx context.Context, conn net.Conn, config *tls.Config, encrypted bool) error {
	if encrypted {
		var request [8]byte
		if _, err := io.ReadFull(conn, request[:]); err != nil {
			return err
		}
		if binary.BigEndian.Uint32(request[4:]) != 80877103 {
			return io.ErrUnexpectedEOF
		}
		if _, err := conn.Write([]byte{'S'}); err != nil {
			return err
		}
		secured := tls.Server(conn, config)
		if err := secured.HandshakeContext(ctx); err != nil {
			return err
		}
		conn = secured
	}
	backend := pgproto3.NewBackend(conn, conn)
	if _, err := backend.ReceiveStartupMessage(); err != nil {
		return err
	}
	backend.Send(&pgproto3.AuthenticationOk{})
	backend.Send(&pgproto3.ParameterStatus{Name: "server_version", Value: "17.0"})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if err := backend.Flush(); err != nil {
		return err
	}
	if _, err := backend.Receive(); err != nil {
		return err
	}
	backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if err := backend.Flush(); err != nil {
		return err
	}
	_, err := backend.Receive()
	return err
}
