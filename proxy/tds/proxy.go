// Package tds provides an observing TDS 7.4 proxy for SQL Server SQL authentication.
package tds

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"github.com/flanksource/sqlglot-go/proxy"
	"io"
	"net"
	"sync"
	"time"
)

const maxMessage = 1 << 20

var bytesReader = bytes.NewReader

type message struct {
	kind         byte
	payload, raw []byte
}

func readMessage(r io.Reader) (message, error) {
	var msg message
	for {
		pkt, err := readTDSPacket(r)
		if err != nil {
			return msg, err
		}
		if len(msg.raw)+len(pkt.Raw) > maxMessage {
			return msg, errors.New("TDS observation exceeds 1 MiB limit")
		}
		if msg.kind != 0 && msg.kind != pkt.Type {
			return msg, errors.New("inconsistent TDS continuation type")
		}
		msg.kind = pkt.Type
		msg.payload = append(msg.payload, pkt.Payload...)
		msg.raw = append(msg.raw, pkt.Raw...)
		if pkt.Status&statusEOM != 0 {
			return msg, nil
		}
	}
}
func Start(ctx context.Context, opts proxy.Options) (*proxy.Server, error) {
	return proxy.Start(ctx, opts, handle)
}
func handle(ctx context.Context, s *proxy.Server, client net.Conn) error {
	server, err := s.Dial(ctx)
	if err != nil {
		return err
	}
	raw := server
	defer s.Release(raw)
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	_ = server.SetDeadline(time.Now().Add(10 * time.Second))
	client, server, err = handshake(ctx, s.Options, client, server)
	if err != nil {
		return err
	}
	login, err := readMessage(client)
	if err != nil {
		return err
	}
	if login.kind != typeLogin7 || len(login.payload) < 94 {
		return errors.New("TDS LOGIN7 required")
	}
	if binary.LittleEndian.Uint32(login.payload[4:]) != 0x74000004 {
		return errors.New("only TDS 7.4 is supported")
	}
	if login.payload[25]&0x80 != 0 || login.payload[27]&0x10 != 0 {
		return errors.New("only TDS SQL authentication is supported")
	}
	if _, err = server.Write(login.raw); err != nil {
		return err
	}
	_ = client.SetDeadline(time.Time{})
	_ = server.SetDeadline(time.Time{})
	session := &session{server: s, label: client.RemoteAddr().String(), prepared: map[int32]string{}}
	defer session.flush()
	return proxy.Relay(client, server, &sniffer{message: session.frontend}, &sniffer{message: session.backend})
}

type sniffer struct {
	buf     []byte
	payload []byte
	kind    byte
	message func(byte, []byte) error
}

func (s *sniffer) Write(b []byte) (int, error) {
	written := len(b)
	s.buf = append(s.buf, b...)
	for len(s.buf) >= 8 {
		size := int(binary.BigEndian.Uint16(s.buf[2:]))
		if size < 8 {
			return 0, errors.New("invalid TDS packet length")
		}
		if len(s.buf) < size {
			break
		}
		if len(s.payload)+size-8 > maxMessage {
			return 0, errors.New("TDS capture exceeds 1 MiB limit")
		}
		s.kind = s.buf[0]
		s.payload = append(s.payload, s.buf[8:size]...)
		end := s.buf[1]&statusEOM != 0
		s.buf = s.buf[size:]
		if end {
			if err := s.message(s.kind, s.payload); err != nil {
				return 0, err
			}
			s.payload = nil
		}
	}
	return written, nil
}

type session struct {
	mu       sync.Mutex
	server   *proxy.Server
	label    string
	pending  []proxy.Statement
	prepared map[int32]string
}

func (s *session) frontend(kind byte, body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := proxy.Statement{Connection: s.label, Driver: "sqlserver", Rows: -1, StartedAt: time.Now()}
	switch kind {
	case typeSQLBatch:
		if len(body) < 4 {
			return errors.New("truncated SQL_BATCH")
		}
		offset := int(binary.LittleEndian.Uint32(body))
		if offset < 4 || offset > len(body) || (len(body)-offset)%2 != 0 {
			return errors.New("invalid SQL_BATCH headers or UTF16")
		}
		row.SQL = decodeUTF16LE(body[offset:])
	case typeRPC:
		name, args, err := decodeRPC(body)
		if err != nil {
			return err
		}
		row.RPC = name
		switch name {
		case "sp_executesql":
			if len(args) > 0 {
				row.SQL, _ = args[0].(string)
			}
		case "sp_prepare", "sp_prepexec":
			if len(args) > 2 {
				row.SQL, _ = args[2].(string)
			}
		case "sp_execute":
			if len(args) > 0 {
				if id, ok := args[0].(int32); ok {
					row.SQL = s.prepared[id]
				}
			}
		case "sp_unprepare":
			if len(args) > 0 {
				if id, ok := args[0].(int32); ok {
					delete(s.prepared, id)
				}
			}
		}
		if s.server.Options.Params {
			row.Args = args
		}
	default:
		return nil
	}
	if len(s.pending) >= 4096 {
		return errors.New("too many outstanding TDS requests")
	}
	s.pending = append(s.pending, row)
	return nil
}
func (s *session) backend(kind byte, body []byte) error {
	if kind != typeTabularResult {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return nil
	}
	row := s.pending[0]
	s.pending = s.pending[1:]
	row.Duration = time.Since(row.StartedAt)
	row.Incomplete = true
	if len(body) >= 13 {
		end := body[len(body)-13:]
		if end[0] == tokenDone || end[0] == tokenDoneProc {
			flags := binary.LittleEndian.Uint16(end[1:])
			row.Incomplete = false
			if flags&0x10 != 0 {
				if _, err := binary.Decode(end[5:], binary.LittleEndian, &row.Rows); err != nil {
					return err
				}
			}
			if flags&0x102 != 0 {
				row.Error = "SQL Server returned an error"
			}
		}
	}
	if row.RPC == "sp_prepare" || row.RPC == "sp_prepexec" {
		if id, ok := returnHandle(body); ok {
			if len(s.prepared) >= 4096 {
				return errors.New("TDS prepared statement state exceeds 4096 entries")
			}
			s.prepared[id] = row.SQL
		} else {
			row.Incomplete = true
		}
	}
	if row.Error != "" {
		if detail := responseError(body); detail != "" {
			row.Error = detail
		}
	}
	s.server.Emit(row)
	return nil
}
func (s *session) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.pending {
		row.Duration = time.Since(row.StartedAt)
		row.Incomplete = true
		s.server.Emit(row)
	}
	s.pending = nil
}
