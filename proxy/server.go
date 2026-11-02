// Package proxy contains shared lifecycle and observation types for database wire proxies.
package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"
)

type Statement struct {
	Connection string        `json:"connection"`
	Driver     string        `json:"driver"`
	SQL        string        `json:"sql,omitempty"`
	RPC        string        `json:"rpc,omitempty"`
	Args       []any         `json:"args,omitempty"`
	StartedAt  time.Time     `json:"startedAt"`
	Duration   time.Duration `json:"duration"`
	Rows       int64         `json:"rows"`
	Error      string        `json:"error,omitempty"`
	Incomplete bool          `json:"incomplete,omitempty"`
}

// Options are copied at startup. Callbacks are serialized and must not call Close.
// Nil UpstreamTLS selects plaintext; non-nil requires verified TLS without fallback.
type Options struct {
	Upstream      string
	Listen        string
	Params        bool
	ClientTLSMode string
	ClientTLS     *tls.Config
	UpstreamTLS   *tls.Config
	OnStatement   func(Statement) error
}

// Handler serves an accepted connection. Dial must be used for tracked upstream connections.
type Handler func(context.Context, *Server, net.Conn) error

type Server struct {
	Options  Options
	listener net.Listener
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	err      error
	wg       sync.WaitGroup
	done     chan struct{}
	emitMu   sync.Mutex
}

func Start(ctx context.Context, opts Options, handler Handler) (*Server, error) {
	if _, _, err := net.SplitHostPort(opts.Upstream); err != nil {
		return nil, fmt.Errorf("upstream must be host:port: %w", err)
	}
	if opts.Listen == "" {
		opts.Listen = "127.0.0.1:0"
	}
	host, port, err := net.SplitHostPort(opts.Listen)
	if err != nil || host == "" {
		return nil, errors.New("listen must specify host:port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return nil, errors.New("invalid listen port")
	}
	if opts.ClientTLSMode == "" {
		opts.ClientTLSMode = "disable"
	}
	switch opts.ClientTLSMode {
	case "allow", "require":
		if opts.ClientTLS == nil || len(opts.ClientTLS.Certificates) == 0 {
			return nil, errors.New("client TLS needs a certificate and key")
		}
	case "disable":
		if opts.ClientTLS != nil {
			return nil, errors.New("client TLS certificate supplied with mode disable")
		}
	default:
		return nil, errors.New("client TLS mode must be disable, allow or require")
	}
	if opts.ClientTLS != nil {
		opts.ClientTLS = opts.ClientTLS.Clone()
		opts.ClientTLS.MinVersion = max(opts.ClientTLS.MinVersion, tls.VersionTLS12)
	}
	if opts.UpstreamTLS != nil {
		opts.UpstreamTLS = opts.UpstreamTLS.Clone()
		if opts.UpstreamTLS.InsecureSkipVerify {
			return nil, errors.New("upstream TLS must verify certificates")
		}
		opts.UpstreamTLS.MinVersion = max(opts.UpstreamTLS.MinVersion, tls.VersionTLS12)
		if opts.UpstreamTLS.ServerName == "" {
			opts.UpstreamTLS.ServerName, _, _ = net.SplitHostPort(opts.Upstream)
		}
	}
	if handler == nil {
		return nil, errors.New("proxy handler is required")
	}
	listener, err := net.Listen("tcp", opts.Listen)
	if err != nil {
		return nil, fmt.Errorf("proxy listen: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Server{Options: opts, listener: listener, ctx: ctx, cancel: cancel, conns: map[net.Conn]struct{}{}, done: make(chan struct{})}
	go s.serve(handler)
	go func() { <-ctx.Done(); s.stop() }()
	return s, nil
}

func (s *Server) Addr() string { return s.listener.Addr().String() }
func (s *Server) Wait() error  { <-s.done; s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func (s *Server) Close() error { s.cancel(); return s.Wait() }
func (s *Server) stop() {
	_ = s.listener.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for conn := range s.conns {
		_ = conn.Close()
	}
}
func (s *Server) track(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		_ = conn.Close()
		return false
	}
	s.conns[conn] = struct{}{}
	return true
}
func (s *Server) release(conn net.Conn) {
	_ = conn.Close()
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
}

// Release closes and removes a connection created with Dial.
func (s *Server) Release(conn net.Conn) { s.release(conn) }
func (s *Server) Dial(ctx context.Context) (net.Conn, error) {
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", s.Options.Upstream)
	if err != nil {
		return nil, err
	}
	if !s.track(conn) {
		return nil, context.Canceled
	}
	return conn, nil
}
func (s *Server) Emit(row Statement) {
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	if s.Options.OnStatement != nil {
		if err := s.Options.OnStatement(row); err != nil {
			s.fail(err)
		}
	}
}
func (s *Server) fail(err error) {
	s.mu.Lock()
	s.err = errors.Join(s.err, err)
	s.mu.Unlock()
	s.cancel()
}
func (s *Server) serve(handler Handler) {
	defer close(s.done)
	defer s.cancel()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if s.ctx.Err() == nil {
				s.fail(err)
			}
			break
		}
		if !s.track(conn) {
			break
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.release(conn)
			if err := handler(s.ctx, s, conn); err != nil && s.ctx.Err() == nil {
				s.Emit(Statement{Connection: conn.RemoteAddr().String(), Rows: -1, StartedAt: time.Now(), Error: err.Error(), Incomplete: true})
			}
		}()
	}
	s.wg.Wait()
}
