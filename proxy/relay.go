package proxy

import (
	"errors"
	"io"
	"net"
)

// Relay observes bytes before forwarding them, so a fast reply cannot precede its request.
func Relay(client, server net.Conn, frontend, backend io.Writer) error {
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.MultiWriter(frontend, server), client)
		if tcp, ok := server.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		} else {
			_ = server.Close()
		}
		done <- err
	}()
	_, err := io.Copy(io.MultiWriter(backend, client), server)
	_ = client.Close()
	_ = server.Close()
	other := <-done
	if errors.Is(err, net.ErrClosed) {
		err = nil
	}
	if errors.Is(other, net.ErrClosed) {
		other = nil
	}
	return errors.Join(err, other)
}
