package tds

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"strings"

	"github.com/flanksource/sqlglot-go/proxy"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const fixtureDir = "testdata/captures"

// Regenerate with `make tds-fixtures`.
var fixturePacketSizes = map[string]int{
	"login_default": 4096, "login_512": 512, "login_32767": 32767, "login_tls": 4096, "batch_literals": 4096,
	"rpc_executesql": 4096, "rpc_types": 4096, "result_types": 4096, "prepared": 4096, "errors_info": 4096,
	"transactions": 4096, "attention": 4096, "temp_set": 4096, "result_storage_types": 4096,
}

// exchanges pairs each request after login with the response that follows it.
type exchange struct {
	request, response message
	packets           int
}

func exchanges(name string) []exchange {
	GinkgoHelper()
	var result []exchange
	for _, item := range loadCapture(fixtureDir, name+".yaml")[4:] {
		msg := item.decode()
		if item.Direction == "client" {
			result = append(result, exchange{request: msg})
			continue
		}
		last := &result[len(result)-1]
		last.response, last.packets = msg, len(item.Packets)
	}
	return result
}

func batchExchange(name, sql string) exchange {
	GinkgoHelper()
	for _, item := range exchanges(name) {
		if item.request.kind == typeSQLBatch && extractSQLFromBatch(item.request.payload) == sql {
			return item
		}
	}
	Fail(fmt.Sprintf("%s has no batch %q", name, sql))
	return exchange{}
}

// tokens lists a response's tokens without the session notices that precede results.
func tokens(payload []byte) []byte {
	GinkgoHelper()
	var found []byte
	Expect(walkTokens(payload, func(token byte, _ []byte) error {
		if token != tokenEnvChange {
			found = append(found, token)
		}
		return nil
	})).To(Succeed())
	return found
}

func finalToken(payload []byte) (byte, uint16) {
	end := payload[len(payload)-13:]
	return end[0], binary.LittleEndian.Uint16(end[1:])
}

var _ = Describe("captured SQL Server traffic", func() {
	It("has exactly the declared fixtures", func() {
		entries, err := os.ReadDir(fixtureDir)
		Expect(err).NotTo(HaveOccurred())
		var names []string
		for _, entry := range entries {
			names = append(names, strings.TrimSuffix(entry.Name(), ".yaml"))
		}
		Expect(names).To(ConsistOf(keys(fixturePacketSizes)))
	})
	for name, packetSize := range fixturePacketSizes {
		It("reassembles "+name+" and contains only placeholder identities", func() {
			messages := loadCapture(fixtureDir, name+".yaml")
			for _, item := range messages {
				raw := item.raw()
				msg := item.decode()
				Expect(msg.raw).To(Equal(raw))
				var observed []byte
				sniff := &sniffer{message: func(_ byte, payload []byte) error { observed = bytes.Clone(payload); return nil }, record: func(recorded []byte) error {
					Expect(recorded).To(Equal(raw))
					return nil
				}}
				for start := 0; start < len(raw); start += 17 {
					_, err := sniff.Write(raw[start:min(start+17, len(raw))])
					Expect(err).NotTo(HaveOccurred())
				}
				Expect(observed).To(HaveLen(len(msg.payload)))
				Expect(bytes.Equal(observed, msg.payload)).To(BeTrue())
			}
			login := messages[2].decode()
			Expect(login.kind).To(Equal(typeLogin7))
			Expect(binary.LittleEndian.Uint32(login.payload[8:])).To(BeEquivalentTo(packetSize))
			Expect([]string{login7Field(login.payload, 36), login7Field(login.payload, 40), login7Password(login.payload), login7Field(login.payload, 48), login7Field(login.payload, 52), login7Field(login.payload, 68)}).
				To(Equal([]string{"example", "acme", "example", "example", "example", "example"}))
		})
	}
	DescribeTable("records the negotiated packet size", func(name, negotiated string) {
		response := loadCapture(fixtureDir, name+".yaml")[3].decode().payload
		Expect(bytes.Contains(response, append([]byte{4}, short(negotiated)...))).To(BeTrue())
	}, Entry("default", "login_default", "4096"), Entry("small", "login_512", "512"), Entry("capped at the TLS record size", "login_32767", "16384"))
	It("offers client TLS only in login_tls and always encrypts upstream", func() {
		for name := range fixturePacketSizes {
			messages := loadCapture(fixtureDir, name+".yaml")
			for i, expected := range []byte{map[bool]byte{true: 1, false: 2}[name == "login_tls"], 1} {
				payload := messages[i].decode().payload
				at, err := option(payload, 1)
				Expect(err).NotTo(HaveOccurred())
				Expect(payload[at]).To(Equal(expected), "%s message %d", name, i)
			}
		}
	})
	It("pairs the literal batch with its sp_executesql form", func() {
		literal := batchExchange("batch_literals", "select * from tbl where a = 'a' and b = 'b'")
		rpc := exchanges("rpc_executesql")
		Expect(rpc).To(HaveLen(1))
		name, args, err := decodeRPC(rpc[0].request.payload)
		Expect(err).NotTo(HaveOccurred())
		Expect(name).To(Equal("sp_executesql"))
		Expect(args).To(Equal([]any{"select * from tbl where a = @p1 and b = @p2", "@p1 nvarchar(1),@p2 nvarchar(1)", "a", "b"}))
		batchEnd, _ := finalToken(literal.response.payload)
		rpcEnd, _ := finalToken(rpc[0].response.payload)
		Expect([]byte{batchEnd, rpcEnd}).To(Equal([]byte{tokenDone, tokenDoneProc}))
		// The row itself is identical in both responses: ROW a='a' b='b' c=1.
		row := []byte{0xd1, 2, 0, 'a', 0, 2, 0, 'b', 0, 4, 1, 0, 0, 0}
		Expect(bytes.Contains(literal.response.payload, row)).To(BeTrue())
		Expect(bytes.Contains(rpc[0].response.payload, row)).To(BeTrue())
		Expect(bytes.Contains(rpc[0].response.payload, []byte{0x79, 0, 0, 0, 0, tokenDoneProc})).To(BeTrue(), "RETURNSTATUS precedes DONEPROC")
	})
	It("decodes every captured RPC parameter type", func() {
		x, a := strings.Repeat("x", 5000), []byte(strings.Repeat("a", 5000))
		expected := []any{
			int32(123), int64(1234567890123), int32(12), int32(7), true, 1.25, float32(2.5), "-123.45", "-123.4500",
			"2025-01-02", "03:04:05.1234567", "2025-01-02T03:04:05.1234567", "2025-01-02T03:04:05.123", "2025-01-02T03:04:05.1234567+02:00",
			"01020304-0506-0708-090a-0b0c0d0e0f10", "example", x, "example", a,
		}
		for range 15 {
			expected = append(expected, nil)
		}
		var actual []any
		for _, item := range exchanges("rpc_types") {
			_, args, err := decodeRPC(item.request.payload)
			Expect(err).NotTo(HaveOccurred())
			Expect(args).To(HaveLen(3))
			actual = append(actual, args[2])
		}
		Expect(actual).To(Equal(expected))
	})
	It("finds every token boundary in every captured response", func() {
		for name := range fixturePacketSizes {
			for i, item := range exchanges(name) {
				if item.response.kind == 0 { // a cancelled request is answered after its ATTENTION
					continue
				}
				found := tokens(item.response.payload)
				Expect(found).NotTo(BeEmpty())
				Expect(found[len(found)-1]).To(BeElementOf(tokenDone, tokenDoneProc), "%s exchange %d", name, i)
			}
		}
	})
	It("rejects truncated responses without reading past them", func() {
		for _, name := range []string{"prepared", "errors_info", "result_storage_types", "batch_literals"} {
			for _, item := range exchanges(name) {
				for end := range item.response.payload {
					_ = walkTokens(item.response.payload[:end], func(byte, []byte) error { return nil })
				}
			}
		}
	})
	It("walks NULL rows, empty results and multi-packet results", func() {
		nulls := batchExchange("result_types", "SELECT CAST(NULL AS int) AS i, CAST(NULL AS nvarchar(10)) AS n; SELECT 1 AS i WHERE 1=0; SELECT 2 AS i")
		Expect(tokens(nulls.response.payload)).To(Equal([]byte{
			tokenColMetadata, tokenNBCRow, tokenDone,
			tokenColMetadata, tokenDone,
			tokenColMetadata, tokenRow, tokenDone,
		}))
		large := batchExchange("result_types", "SELECT CAST(REPLICATE(CAST(N'x' AS nvarchar(max)),60000) AS nvarchar(max)) AS large_value")
		Expect(large.packets).To(BeNumerically(">=", 30))
		Expect(tokens(large.response.payload)).To(Equal([]byte{tokenColMetadata, tokenRow, tokenDone}))
	})
	It("walks fixed-size, padded, text pointer, variant, XML and CLR values", func() {
		items := exchanges("result_storage_types")
		found := tokens(items[len(items)-1].response.payload)
		Expect(found).To(ContainElements(tokenColMetadata, tokenRow, tokenDone))
		rows := 0
		for _, token := range found {
			if token == tokenRow || token == tokenNBCRow {
				rows++
			}
		}
		Expect(rows).To(Equal(2))
	})
	It("reads prepared statement handles that follow result metadata", func() {
		var names []string
		var handles []int32
		var statements []proxy.Statement
		observed := &session{prepared: map[int32]string{}, server: &proxy.Server{Options: proxy.Options{OnStatement: func(row proxy.Statement) error {
			statements = append(statements, row)
			return nil
		}}}}
		for _, item := range exchanges("prepared") {
			name, _, err := decodeRPC(item.request.payload)
			Expect(err).NotTo(HaveOccurred())
			names = append(names, name)
			if handle, ok := returnHandle(item.response.payload); ok {
				handles = append(handles, handle)
			}
			Expect(observed.frontend(item.request.kind, item.request.payload)).To(Succeed())
			Expect(observed.backend(item.response.kind, item.response.payload)).To(Succeed())
		}
		Expect(names).To(Equal([]string{"sp_prepare", "sp_execute", "sp_unprepare", "sp_prepexec"}))
		Expect(handles).To(Equal([]int32{1, 2}))
		Expect(statements).To(HaveLen(4))
		Expect(statements[1].SQL).To(Equal("select @p1"), "sp_execute is reported with the SQL its handle was prepared from")
		for _, statement := range statements {
			Expect(statement.Incomplete).To(BeFalse(), statement.RPC)
		}
	})
	It("reads errors from batches and from RPCs that already returned metadata", func() {
		items := exchanges("errors_info")
		Expect(items).To(HaveLen(4))
		Expect(responseError(items[0].response.payload)).To(Equal("Incorrect syntax near the keyword 'FROM'."))
		Expect(responseError(items[1].response.payload)).To(HavePrefix("Violation of PRIMARY KEY constraint 'pk_capture_unique'."))
		Expect(responseError(items[2].response.payload)).To(BeEmpty(), "PRINT and RAISERROR level 10 are informational")
		for _, text := range []string{"example info", "example nowait"} {
			Expect(bytes.Contains(items[2].response.payload, encodeUTF16LE(text))).To(BeTrue())
		}
		Expect(responseError(items[3].response.payload)).To(Equal("Conversion failed when converting the nvarchar value 'invalid' to data type int."))
	})
	It("records transaction manager requests", func() {
		var requests []uint16
		for _, item := range exchanges("transactions") {
			if item.request.kind == typeTransMgrRequest {
				body := item.request.payload
				requests = append(requests, binary.LittleEndian.Uint16(body[binary.LittleEndian.Uint32(body):]))
			}
		}
		Expect(requests).To(Equal([]uint16{5, 7, 5, 8}), "begin, commit, begin, rollback")
	})
	It("records cancellation with the DONE attention flag", func() {
		items := exchanges("attention")
		Expect(items[len(items)-1].request.kind).To(Equal(typeAttention))
		kind, flags := finalToken(items[len(items)-1].response.payload)
		Expect(kind).To(Equal(tokenDone))
		Expect(flags & 0x20).To(BeEquivalentTo(0x20))
	})
	It("records the session-scoped batch a rewrite must refuse", func() {
		items := exchanges("temp_set")
		Expect(items).To(HaveLen(1))
		Expect(extractSQLFromBatch(items[0].request.payload)).To(ContainSubstring("CREATE TABLE #capture_tmp"))
	})
})

func keys(values map[string]int) []string {
	var result []string
	for key := range values {
		result = append(result, key)
	}
	return result
}
