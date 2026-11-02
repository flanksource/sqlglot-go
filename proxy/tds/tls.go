package tds

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/flanksource/sqlglot-go/proxy"
	"io"
	"net"
)

// During TDS 7.4 negotiation only, TLS records travel inside PRELOGIN packets.
type tlsTransport struct {
	net.Conn
	framed  bool
	pending []byte
}

func (c *tlsTransport) Read(b []byte) (int, error) {
	if len(c.pending) > 0 {
		n := copy(b, c.pending)
		c.pending = c.pending[n:]
		return n, nil
	}
	if !c.framed {
		return c.Conn.Read(b)
	}
	pkt, err := readTDSPacket(c.Conn)
	if err != nil {
		return 0, err
	}
	if pkt.Type != typePrelogin && pkt.Type != typeTabularResult {
		return 0, errors.New("unexpected TDS TLS handshake packet")
	}
	c.pending = pkt.Payload
	return c.Read(b)
}
func (c *tlsTransport) Write(b []byte) (int, error) {
	if !c.framed {
		return c.Conn.Write(b)
	}
	_, err := io.Copy(c.Conn, bytesReader(buildTDSPacket(typePrelogin, statusEOM, b)))
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

func upgrade(ctx context.Context, conn net.Conn, config *tls.Config, server bool) (net.Conn, error) {
	transport := &tlsTransport{Conn: conn, framed: true}
	secured := tls.Client(transport, config)
	if server {
		secured = tls.Server(transport, config)
	}
	if err := secured.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	transport.framed = false
	return secured, nil
}

func option(payload []byte, kind byte) (int, error) {
	for pos := 0; pos < len(payload); pos += 5 {
		if payload[pos] == 0xff {
			return -1, nil
		}
		if pos+5 > len(payload) {
			return -1, errors.New("truncated TDS PRELOGIN option")
		}
		offset := int(binary.BigEndian.Uint16(payload[pos+1:]))
		size := int(binary.BigEndian.Uint16(payload[pos+3:]))
		if offset+size > len(payload) || size == 0 {
			return -1, errors.New("invalid TDS PRELOGIN option bounds")
		}
		if payload[pos] == kind {
			return offset, nil
		}
	}
	return -1, errors.New("unterminated TDS PRELOGIN options")
}

func handshake(ctx context.Context, opts proxy.Options, client, server net.Conn) (net.Conn, net.Conn, error) {
	request, err := readMessage(client)
	if err != nil {
		return client, server, err
	}
	if request.kind != typePrelogin {
		return client, server, errors.New("TDS 7.4 PRELOGIN required; TDS 8 is unsupported")
	}
	enc, err := option(request.payload, 1)
	if err != nil || enc < 0 {
		return client, server, errors.New("TDS PRELOGIN encryption option required")
	}
	mars, err := option(request.payload, 4)
	if err != nil {
		return client, server, err
	}
	if mars >= 0 && request.payload[mars] != 0 {
		return client, server, errors.New("TDS MARS is unsupported")
	}
	original := request.payload[enc]
	useTLS := opts.ClientTLSMode != "disable" && original != 2
	if opts.ClientTLSMode == "require" && !useTLS {
		return client, server, errors.New("client TLS required")
	}
	if opts.ClientTLSMode == "disable" && original != 0 && original != 2 {
		return client, server, errors.New("client requested TLS but client TLS mode is disable")
	}
	request.payload[enc] = 2
	if opts.UpstreamTLS != nil {
		request.payload[enc] = 1
	}
	if _, err = server.Write(buildTDSPacket(typePrelogin, statusEOM, request.payload)); err != nil {
		return client, server, err
	}
	response, err := readMessage(server)
	if err != nil {
		return client, server, err
	}
	idx, err := option(response.payload, 1)
	if err != nil || idx < 0 {
		return client, server, errors.New("upstream PRELOGIN encryption option required")
	}
	upstreamMode := response.payload[idx]
	if opts.UpstreamTLS != nil {
		if upstreamMode != 1 && upstreamMode != 3 {
			return client, server, errors.New("upstream refused TDS TLS")
		}
		server, err = upgrade(ctx, server, opts.UpstreamTLS, false)
		if err != nil {
			return client, server, fmt.Errorf("upstream TLS: %w", err)
		}
	} else if upstreamMode != 2 && upstreamMode != 0 {
		return client, server, errors.New("upstream requires TLS; configure upstream TLS")
	}
	response.payload[idx] = 2
	if useTLS {
		response.payload[idx] = 1
	}
	if _, err = client.Write(buildTDSPacket(typeTabularResult, statusEOM, response.payload)); err != nil {
		return client, server, err
	}
	if useTLS {
		client, err = upgrade(ctx, client, opts.ClientTLS, true)
	}
	return client, server, err
}
