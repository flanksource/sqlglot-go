package tds

import (
	"encoding/binary"
	"fmt"
	"github.com/flanksource/sqlglot-go/proxy"
	"github.com/flanksource/sqlglot-go/proxy/internal/testcert"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"net"
	"testing"
)

func TestTDS(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "TDS proxy") }
func prelogin(enc byte) []byte {
	return []byte{0, 0, 16, 0, 6, 1, 0, 22, 0, 1, 4, 0, 23, 0, 1, 255, 15, 0, 0, 0, 0, 0, enc, 0}
}
func batch(sql string) []byte { return append([]byte{4, 0, 0, 0}, encodeUTF16LE(sql)...) }

var _ = Describe("TDS relay", func() {
	for _, clientTLS := range []bool{false, true} {
		for _, upstreamTLS := range []bool{false, true} {
			It(fmt.Sprintf("supports TLS client=%t upstream=%t", clientTLS, upstreamTLS), func(ctx SpecContext) {
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
					request, err := readMessage(conn)
					if err != nil {
						done <- err
						return
					}
					if request.kind != typePrelogin {
						done <- fmt.Errorf("wrong prelogin type")
						return
					}
					enc := byte(2)
					if upstreamTLS {
						enc = 1
					}
					if _, err = conn.Write(buildTDSPacket(typeTabularResult, statusEOM, prelogin(enc))); err != nil {
						done <- err
						return
					}
					if upstreamTLS {
						conn, err = upgrade(ctx, conn, cert, true)
						if err != nil {
							done <- err
							return
						}
					}
					if _, err = readMessage(conn); err != nil {
						done <- err
						return
					}
					response := make([]byte, 13)
					response[0] = tokenDone
					if _, err = conn.Write(buildTDSPacket(typeTabularResult, statusEOM, response)); err != nil {
						done <- err
						return
					}
					query, err := readMessage(conn)
					if err != nil {
						done <- err
						return
					}
					if extractSQLFromBatch(query.payload) != "SELECT 1" {
						done <- fmt.Errorf("query changed")
						return
					}
					binary.LittleEndian.PutUint16(response[1:], 0x10)
					binary.LittleEndian.PutUint64(response[5:], 1)
					_, err = conn.Write(buildTDSPacket(typeTabularResult, statusEOM, response))
					done <- err
				}()
				captured := make(chan proxy.Statement, 8)
				opts := proxy.Options{Upstream: listener.Addr().String(), OnStatement: func(row proxy.Statement) error { captured <- row; return nil }}
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
				client, err := net.Dial("tcp", server.Addr())
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(func() { _ = client.Close() })
				enc := byte(2)
				if clientTLS {
					enc = 1
				}
				_, err = client.Write(buildTDSPacket(typePrelogin, statusEOM, prelogin(enc)))
				Expect(err).NotTo(HaveOccurred())
				_, err = readMessage(client)
				Expect(err).NotTo(HaveOccurred())
				if clientTLS {
					client, err = upgrade(ctx, client, trust, false)
					Expect(err).NotTo(HaveOccurred())
				}
				login := make([]byte, 94)
				binary.LittleEndian.PutUint32(login[4:], 0x74000004)
				_, err = client.Write(buildTDSPacket(typeLogin7, statusEOM, login))
				Expect(err).NotTo(HaveOccurred())
				_, err = readMessage(client)
				Expect(err).NotTo(HaveOccurred())
				_, err = client.Write(buildTDSPacket(typeSQLBatch, statusEOM, batch("SELECT 1")))
				Expect(err).NotTo(HaveOccurred())
				_, err = readMessage(client)
				Expect(err).NotTo(HaveOccurred())
				Expect(<-done).To(Succeed())
				var row proxy.Statement
				Eventually(captured).Should(Receive(&row))
				Expect(row.SQL).To(Equal("SELECT 1"))
				Expect(row.Rows).To(Equal(int64(1)))
				Expect(row.Incomplete).To(BeFalse())
			})
		}
	}
	It("reassembles UTF16 queries across arbitrary packet and socket boundaries", func() {
		sql := "SELECT N'שלום'"
		raw := buildTDSPacket(typeSQLBatch, statusEOM, batch(sql))
		var got string
		sniff := &sniffer{message: func(_ byte, payload []byte) error { got = extractSQLFromBatch(payload); return nil }}
		for _, b := range raw {
			_, err := sniff.Write([]byte{b})
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(got).To(Equal(sql))
	})
	It("rejects malformed framing", func() { _, err := (&sniffer{}).Write(make([]byte, 8)); Expect(err).To(HaveOccurred()) })
	It("joins continuation packets before decoding SQL", func() {
		payload := batch("SELECT 1")
		var got string
		sniff := &sniffer{message: func(_ byte, body []byte) error { got = extractSQLFromBatch(body); return nil }}
		_, err := sniff.Write(buildTDSPacket(typeSQLBatch, statusNormal, payload[:6]))
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(BeEmpty())
		_, err = sniff.Write(buildTDSPacket(typeSQLBatch, statusEOM, payload[6:]))
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal("SELECT 1"))
	})
	It("decodes sp_executesql without exposing arguments by default", func() {
		payload := append([]byte{4, 0, 0, 0, 255, 255, 10, 0, 0, 0, 0, 0, 0xe7, 100, 0, 0, 0, 0, 0, 0}, byte(len(encodeUTF16LE("SELECT @p"))), 0)
		payload = append(payload, encodeUTF16LE("SELECT @p")...)
		name, args, err := decodeRPC(payload)
		Expect(err).NotTo(HaveOccurred())
		Expect(name).To(Equal("sp_executesql"))
		Expect(args).To(Equal([]any{"SELECT @p"}))
	})
})
