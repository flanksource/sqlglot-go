package tds

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/flanksource/sqlglot-go/proxy"
	"github.com/flanksource/sqlglot-go/proxy/internal/testcert"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

const (
	testLoginPhrase = "example login phrase"
	testUser        = "svc-reporting"
	testHost        = "workstation-17"
	testServer      = "SQLNODE-04"
	testDatabase    = "ledger_main"
	testQuery       = "SELECT 'ledger_main'"
)

func loadCapture(dir, name string) []capturedMessage {
	GinkgoHelper()
	data, err := os.DirFS(dir).(interface {
		ReadFile(string) ([]byte, error)
	}).ReadFile(name)
	Expect(err).NotTo(HaveOccurred())
	var messages []capturedMessage
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	Expect(decoder.Decode(&messages)).To(Succeed())
	return messages
}

func (m capturedMessage) raw() []byte {
	GinkgoHelper()
	var raw []byte
	for _, encoded := range m.Packets {
		packet, err := hex.DecodeString(encoded)
		Expect(err).NotTo(HaveOccurred())
		raw = append(raw, packet...)
	}
	return raw
}

func (m capturedMessage) decode() message {
	GinkgoHelper()
	msg, err := readMessage(bytes.NewReader(m.raw()))
	Expect(err).NotTo(HaveOccurred())
	return msg
}

func login7Field(payload []byte, position int) string {
	offset, size := int(binary.LittleEndian.Uint16(payload[position:])), 2*int(binary.LittleEndian.Uint16(payload[position+2:]))
	return decodeUTF16LE(payload[offset : offset+size])
}

func login7Password(payload []byte) string {
	offset, size := int(binary.LittleEndian.Uint16(payload[44:])), 2*int(binary.LittleEndian.Uint16(payload[46:]))
	password := bytes.Clone(payload[offset : offset+size])
	for i, b := range password {
		b ^= 0xa5
		password[i] = b<<4 | b>>4
	}
	return decodeUTF16LE(password)
}

// testLogin7 builds LOGIN7 with every identifying field populated.
func testLogin7() []byte {
	payload := make([]byte, login7FixedSize)
	binary.LittleEndian.PutUint32(payload[4:], 0x74000004)
	copy(payload[16:20], []byte{9, 9, 9, 9})
	copy(payload[72:78], []byte{1, 2, 3, 4, 5, 6})
	for _, field := range []struct {
		position int
		value    string
	}{{36, testHost}, {40, testUser}, {44, testLoginPhrase}, {48, "billing-app"}, {52, testServer}, {60, "driver"}, {64, "us_english"}, {68, testDatabase}} {
		encoded := encodeUTF16LE(field.value)
		if field.position == 44 {
			for i, b := range encoded {
				encoded[i] = (b<<4 | b>>4) ^ 0xa5
			}
		}
		Expect(binary.Encode(payload[field.position:], binary.LittleEndian, []uint16{uint16(len(payload) & 0xffff), uint16(len(encoded) / 2 & 0xffff)})).Error().NotTo(HaveOccurred())
		payload = append(payload, encoded...)
	}
	return payload
}

func token(kind byte, parts ...[]byte) []byte {
	data := bytes.Join(parts, nil)
	return append(binary.LittleEndian.AppendUint16([]byte{kind}, uint16(len(data)&0xffff)), data...)
}

func short(value string) []byte {
	return append([]byte{byte(len(value) & 0xff)}, encodeUTF16LE(value)...)
}

func infoToken(kind byte, text string) []byte {
	return token(kind, []byte{1, 0, 0, 0, 1, 14, byte(len(text) & 0xff), 0}, encodeUTF16LE(text), short(testServer), []byte{0, 1, 0, 0, 0})
}

var done = []byte{tokenDone, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}

// fakeSQLServer answers one login and one batch over TLS.
func fakeSQLServer(ctx context.Context, listener net.Listener, config *tls.Config) error {
	conn, err := listener.Accept()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := readMessage(conn); err != nil {
		return err
	}
	if _, err := conn.Write(buildTDSPacket(typeTabularResult, statusEOM, prelogin(1))); err != nil {
		return err
	}
	secured, err := upgrade(ctx, conn, config, true)
	if err != nil {
		return err
	}
	conn = secured
	login, err := readMessage(conn)
	if err != nil {
		return err
	}
	if login7Password(login.payload) != testLoginPhrase {
		return fmt.Errorf("login was altered in transit")
	}
	response := bytes.Join([][]byte{
		token(tokenEnvChange, []byte{1}, short(testDatabase), short("master")),
		infoToken(tokenInfo, "Changed database context to '"+testDatabase+"'."),
		done,
	}, nil)
	if _, err := conn.Write(buildTDSPacket(typeTabularResult, statusEOM, response)); err != nil {
		return err
	}
	if _, err := readMessage(conn); err != nil {
		return err
	}
	_, err = conn.Write(buildTDSPacket(typeTabularResult, statusEOM, append(infoToken(tokenError, "Invalid object name"), done...)))
	return err
}

var _ = Describe("TDS wire capture", func() {
	It("records decrypted messages without credentials or server identity", func(ctx SpecContext) {
		config, _, err := testcert.Configs()
		Expect(err).NotTo(HaveOccurred())
		dir := GinkgoT().TempDir()
		certificate := filepath.Join(dir, "upstream.pem")
		Expect(os.WriteFile(certificate, testcert.PEM(config), 0o600)).To(Succeed())
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(listener.Close)
		finished := make(chan error, 1)
		go func() { finished <- fakeSQLServer(ctx, listener, config) }()
		captures := filepath.Join(dir, "captures")
		server, err := Start(ctx, proxy.Options{Upstream: listener.Addr().String(), UpstreamCertificate: certificate, CaptureDir: captures})
		Expect(err).NotTo(HaveOccurred())
		client, err := net.Dial("tcp", server.Addr())
		Expect(err).NotTo(HaveOccurred())
		for _, packet := range [][]byte{
			buildTDSPacket(typePrelogin, statusEOM, prelogin(2)),
			buildTDSPacket(typeLogin7, statusEOM, testLogin7()),
			buildTDSPacket(typeSQLBatch, statusEOM, batch(testQuery)),
		} {
			_, err = client.Write(packet)
			Expect(err).NotTo(HaveOccurred())
			_, err = readMessage(client)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(<-finished).To(Succeed())
		Expect(client.Close()).To(Succeed())
		Expect(server.Close()).To(Succeed())

		entries, err := os.ReadDir(captures)
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(HaveLen(1))
		info, err := entries[0].Info()
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		messages := loadCapture(captures, entries[0].Name())
		var types []string
		for _, message := range messages {
			types = append(types, message.Direction+" "+message.Type)
			for _, identity := range []string{testLoginPhrase, testUser, testHost, testServer, "billing-app"} {
				Expect(bytes.Contains(message.raw(), encodeUTF16LE(identity))).To(BeFalse(), "%s in %s", identity, message.Type)
			}
		}
		Expect(types).To(Equal([]string{
			"client PRELOGIN (0x12)", "server TABULAR_RESULT (0x04)", "client LOGIN7 (0x10)",
			"server TABULAR_RESULT (0x04)", "client SQL_BATCH (0x01)", "server TABULAR_RESULT (0x04)",
		}))
		login := messages[2].decode().payload
		Expect(int(binary.LittleEndian.Uint32(login))).To(Equal(len(login)))
		Expect(login[72:78]).To(Equal(make([]byte, 6)))
		Expect(login[16:20]).To(Equal(make([]byte, 4)))
		Expect([]string{login7Field(login, 36), login7Field(login, 40), login7Password(login), login7Field(login, 48), login7Field(login, 52), login7Field(login, 64), login7Field(login, 68)}).
			To(Equal([]string{"example", "acme", "example", "example", "example", "us_english", "example"}))
		loginResponse := messages[3].decode().payload
		Expect(bytes.Contains(loginResponse, encodeUTF16LE(testDatabase))).To(BeFalse())
		Expect(bytes.Contains(loginResponse, encodeUTF16LE("Changed database context to 'exampleexam'."))).To(BeTrue())
		Expect(bytes.Contains(loginResponse, short("master"))).To(BeTrue())
		Expect(messages[4].Note).To(Equal(testQuery), "statements are recorded as sent")
		Expect(bytes.Contains(messages[5].decode().payload, short("exampleexa"))).To(BeTrue(), "server name is replaced in later tokens")
	})
	It("rejects an upstream that presents a different certificate", func(ctx SpecContext) {
		config, _, err := testcert.Configs()
		Expect(err).NotTo(HaveOccurred())
		other, _, err := testcert.Configs()
		Expect(err).NotTo(HaveOccurred())
		certificate := filepath.Join(GinkgoT().TempDir(), "other.pem")
		Expect(os.WriteFile(certificate, testcert.PEM(other), 0o600)).To(Succeed())
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(listener.Close)
		finished := make(chan error, 1)
		go func() { finished <- fakeSQLServer(ctx, listener, config) }()
		rows := make(chan proxy.Statement, 1)
		server, err := Start(ctx, proxy.Options{Upstream: listener.Addr().String(), UpstreamCertificate: certificate, OnStatement: func(row proxy.Statement) error { rows <- row; return nil }})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(server.Close)
		client, err := net.Dial("tcp", server.Addr())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = client.Close() })
		_, err = client.Write(buildTDSPacket(typePrelogin, statusEOM, prelogin(2)))
		Expect(err).NotTo(HaveOccurred())
		var row proxy.Statement
		Eventually(rows).Should(Receive(&row))
		Expect(row.Error).To(ContainSubstring("doesn't match"))
		Expect(<-finished).To(HaveOccurred())
	})
	It("refuses a pinned certificate combined with other TLS settings", func(ctx SpecContext) {
		_, trust, err := testcert.Configs()
		Expect(err).NotTo(HaveOccurred())
		_, err = Start(ctx, proxy.Options{Upstream: "localhost:1433", UpstreamTLS: trust, UpstreamCertificate: "upstream.pem"})
		Expect(err).To(MatchError(ContainSubstring("cannot be combined")))
	})
	It("fails instead of recording a login response it cannot scrub", func() {
		scrub := scrubber{loginSent: true}
		Expect(scrub.message("server", typeTabularResult, []byte{0xed, 1, 0, 0})).To(MatchError(ContainSubstring("login response: unsupported TDS token 0xed")))
	})
	It("preserves packet boundaries when scrubbing a continued message", func() {
		payload := append(infoToken(tokenInfo, "hello"), done...)
		raw := append(buildTDSPacket(typeTabularResult, statusNormal, payload[:30]), buildTDSPacket(typeTabularResult, statusEOM, payload[30:])...)
		packets, joined, err := splitPackets(raw)
		Expect(err).NotTo(HaveOccurred())
		scrub := scrubber{loginSent: true}
		Expect(scrub.message("server", typeTabularResult, joined)).To(Succeed())
		refill(packets, joined)
		Expect([]int{len(packets[0]), len(packets[1])}).To(Equal([]int{38, len(payload) - 30 + 8}))
		Expect(bytes.Contains(bytes.Join([][]byte{packets[0][8:], packets[1][8:]}, nil), short(testServer))).To(BeFalse())
	})
})
