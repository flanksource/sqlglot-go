// Package postgres provides an observing PostgreSQL wire proxy, derived from
// flanksource/telltale pkg/tracing/pgproxy (507c01f9be71).
package postgres

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/flanksource/sqlglot-go/proxy"
	"io"
	"net"
	"time"
)

const driver = "postgres"

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
	client, server, err = startup(ctx, s.Options, client, server)
	if err != nil {
		return err
	}
	_ = client.SetDeadline(time.Time{})
	_ = server.SetDeadline(time.Time{})
	session := newPGSession(client.RemoteAddr().String(), s.Options.Params, s.Emit)
	defer session.flush()
	return proxy.Relay(client, server, observe(session, true), observe(session, false))
}

func startup(ctx context.Context, opts proxy.Options, client, server net.Conn) (net.Conn, net.Conn, error) {
	if opts.UpstreamTLS != nil {
		request := make([]byte, 8)
		binary.BigEndian.PutUint32(request, 8)
		binary.BigEndian.PutUint32(request[4:], 80877103)
		if _, err := server.Write(request); err != nil {
			return client, server, err
		}
		var response [1]byte
		if _, err := io.ReadFull(server, response[:]); err != nil {
			return client, server, err
		}
		if response[0] != 'S' {
			return client, server, errors.New("upstream refused PostgreSQL TLS")
		}
		secured := tls.Client(server, opts.UpstreamTLS)
		if err := secured.HandshakeContext(ctx); err != nil {
			return client, server, fmt.Errorf("upstream TLS: %w", err)
		}
		server = secured
	}
	encrypted := false
	for {
		var header [8]byte
		if _, err := io.ReadFull(client, header[:]); err != nil {
			return client, server, err
		}
		size := int(binary.BigEndian.Uint32(header[:4]))
		code := binary.BigEndian.Uint32(header[4:])
		if size < 8 || size > maxWireMessage {
			return client, server, errors.New("invalid PostgreSQL startup length")
		}
		if code == 80877103 || code == 80877104 {
			if size != 8 || encrypted {
				return client, server, errors.New("invalid repeated TLS negotiation")
			}
			if code == 80877103 && opts.ClientTLSMode != "disable" {
				if _, err := client.Write([]byte{'S'}); err != nil {
					return client, server, err
				}
				secured := tls.Server(client, opts.ClientTLS)
				if err := secured.HandshakeContext(ctx); err != nil {
					return client, server, fmt.Errorf("client TLS: %w", err)
				}
				client = secured
				encrypted = true
			} else {
				if _, err := client.Write([]byte{'N'}); err != nil {
					return client, server, err
				}
			}
			continue
		}
		if opts.ClientTLSMode == "require" && !encrypted && code != 80877102 {
			return client, server, errors.New("client TLS required")
		}
		packet := make([]byte, size)
		copy(packet, header[:])
		if _, err := io.ReadFull(client, packet[8:]); err != nil {
			return client, server, err
		}
		if _, err := server.Write(packet); err != nil {
			return client, server, err
		}
		return client, server, nil
	}
}
