package postgres

import (
	"bytes"
	"errors"
	"github.com/flanksource/sqlglot-go/proxy"
	"github.com/jackc/pgx/v5/pgproto3"
)

func observe(session *pgSession, frontend bool) *pgSniffer {
	sniff := &pgSniffer{}
	sniff.onMessage = func(kind byte, body []byte) {
		var err error
		session.mu.Lock()
		limited := (kind == 'Q' || kind == 'E') && len(session.pending) >= 4096 || kind == 'P' && len(session.prepared) >= 4096 || kind == 'B' && len(session.portals) >= 4096
		session.mu.Unlock()
		if frontend && limited {
			sniff.err = errors.New("PostgreSQL observation state exceeds 4096 entries")
			return
		}
		if frontend {
			switch kind {
			case 'p':
				if bytes.HasPrefix(body, []byte("SCRAM-SHA-256-PLUS\x00")) {
					err = errors.New("PostgreSQL channel binding is unsupported; configure channel_binding=disable")
				}
			case 'Q':
				var msg pgproto3.Query
				err = msg.Decode(body)
			case 'P':
				var msg pgproto3.Parse
				err = msg.Decode(body)
			case 'B':
				var msg pgproto3.Bind
				err = msg.Decode(body)
			case 'E':
				var msg pgproto3.Execute
				err = msg.Decode(body)
			case 'C':
				var msg pgproto3.Close
				err = msg.Decode(body)
				if err == nil {
					session.mu.Lock()
					if msg.ObjectType == 'S' {
						delete(session.prepared, msg.Name)
					} else {
						delete(session.portals, msg.Name)
					}
					session.mu.Unlock()
				}
			}
			if err == nil {
				session.frontend(kind, body)
			}
		} else {
			switch kind {
			case 'C':
				var msg pgproto3.CommandComplete
				err = msg.Decode(body)
			case 'E':
				var msg pgproto3.ErrorResponse
				err = msg.Decode(body)
			}
			if err == nil {
				session.backend(kind, body)
			}
		}
		sniff.err = err
	}
	sniff.onSkip = func() {
		session.emit(proxy.Statement{Connection: session.label, Driver: driver, Rows: -1, Incomplete: true, Error: "PostgreSQL observation exceeded 1 MiB"})
	}
	return sniff
}
