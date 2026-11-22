package tds

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

// capturedMessage is one decrypted TDS message with its original packet boundaries.
type capturedMessage struct {
	Direction string   `yaml:"direction"`
	Type      string   `yaml:"type"`
	Note      string   `yaml:"note,omitempty"`
	Packets   []string `yaml:"packets"`
}

var messageNames = map[byte]string{
	typeSQLBatch: "SQL_BATCH", typeRPC: "RPC", typeTabularResult: "TABULAR_RESULT", typeAttention: "ATTENTION",
	typeTransMgrRequest: "TRANS_MGR_REQ", typePrelogin: "PRELOGIN", typeLogin7: "LOGIN7",
}

// captureFiles names one file per connection in accept order.
type captureFiles struct {
	dir, prefix string
	connections atomic.Int64
}

func newCaptureFiles(dir string) (*captureFiles, error) {
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("capture directory: %w", err)
	}
	return &captureFiles{dir: dir, prefix: time.Now().UTC().Format("20060102T150405.000000000Z")}, nil
}

// open returns nil when capture is disabled; a nil capture records nothing.
func (f *captureFiles) open() (*capture, error) {
	if f == nil {
		return nil, nil
	}
	root, err := os.OpenRoot(f.dir)
	if err != nil {
		return nil, fmt.Errorf("capture directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	name := fmt.Sprintf("%s-%06d.yaml", f.prefix, f.connections.Add(1))
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("capture file: %w", err)
	}
	return &capture{file: file}, nil
}

type capture struct {
	mu    sync.Mutex
	file  *os.File
	scrub scrubber
}

func (c *capture) client(raw []byte) error { return c.record("client", raw) }
func (c *capture) server(raw []byte) error { return c.record("server", raw) }

func (c *capture) record(direction string, raw []byte) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	packets, payload, err := splitPackets(raw)
	if err != nil {
		return fmt.Errorf("capture: %w", err)
	}
	kind := packets[0][0]
	if kind == typeLogin7 {
		packets, err = c.scrub.login(packets[0], payload)
	} else {
		err = c.scrub.message(direction, kind, payload)
		refill(packets, payload)
	}
	if err != nil {
		return fmt.Errorf("capture: %w", err)
	}
	name := messageNames[kind]
	if name == "" {
		name = "UNKNOWN"
	}
	item := capturedMessage{Direction: direction, Type: fmt.Sprintf("%s (0x%02x)", name, kind), Note: captureNote(kind, payload)}
	for _, packet := range packets {
		item.Packets = append(item.Packets, hex.EncodeToString(packet))
	}
	encoded, err := yaml.Marshal([]capturedMessage{item})
	if err != nil {
		return fmt.Errorf("capture: %w", err)
	}
	if _, err := c.file.Write(encoded); err != nil {
		return fmt.Errorf("capture: %w", err)
	}
	return nil
}

func (c *capture) close() error {
	if c == nil {
		return nil
	}
	return c.file.Close()
}

func captureNote(kind byte, payload []byte) string {
	switch kind {
	case typeSQLBatch:
		return extractSQLFromBatch(payload)
	case typeRPC:
		if name, _, err := decodeRPC(payload); err == nil {
			return name
		}
	}
	return ""
}

// splitPackets copies a message into its packets and their joined payload.
func splitPackets(raw []byte) (packets [][]byte, payload []byte, err error) {
	for len(raw) > 0 {
		if len(raw) < tdsHeaderSize {
			return nil, nil, errors.New("truncated TDS packet")
		}
		n := int(binary.BigEndian.Uint16(raw[2:]))
		if n < tdsHeaderSize || n > len(raw) {
			return nil, nil, errors.New("invalid TDS packet length")
		}
		packets = append(packets, append([]byte(nil), raw[:n]...))
		payload = append(payload, raw[tdsHeaderSize:n]...)
		raw = raw[n:]
	}
	if len(packets) == 0 {
		return nil, nil, errors.New("empty TDS message")
	}
	return packets, payload, nil
}

// refill writes a same-length payload back across the original packets.
func refill(packets [][]byte, payload []byte) {
	for _, packet := range packets {
		payload = payload[copy(packet[tdsHeaderSize:], payload):]
	}
}
