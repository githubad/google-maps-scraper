package testtls

import (
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Chooser picks the certificate for a connection: index is the order in which
// the stub accepted it (0 for the first), sni the server name the client sent.
type Chooser func(index int, sni string) *tls.Certificate

// Always returns a Chooser that presents the same certificate on every connection.
func Always(cert *tls.Certificate) Chooser {
	return func(int, string) *tls.Certificate { return cert }
}

// Stub is a local HTTPS server standing in for Google. It records the source
// address and server name of every connection it accepts, which is how a test
// proves that nothing reached it except through the proxy.
type Stub struct {
	Port int

	chooser Chooser
	mu      sync.Mutex
	remotes []string
	snis    []string
	next    int
}

// NewStub starts a stub on 127.0.0.1 serving handler; it is closed with the test.
func NewStub(tb testing.TB, chooser Chooser, handler http.Handler) *Stub {
	tb.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(tb, err)

	addr, ok := ln.Addr().(*net.TCPAddr)
	require.True(tb, ok)

	s := &Stub{chooser: chooser, Port: addr.Port}

	cfg := &tls.Config{
		NextProtos: []string{"http/1.1"},
		GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			idx := -1
			if rc, ok := h.Conn.(*recConn); ok {
				idx = rc.index
			}

			s.mu.Lock()
			s.snis = append(s.snis, h.ServerName)
			s.mu.Unlock()

			return &tls.Config{
				Certificates: []tls.Certificate{*s.chooser(idx, h.ServerName)},
				NextProtos:   []string{"http/1.1"},
			}, nil
		},
	}

	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}

	go func() { _ = srv.Serve(tls.NewListener(&recordingListener{Listener: ln, s: s}, cfg)) }()

	tb.Cleanup(func() { _ = srv.Close() })

	return s
}

// Addr is the address the proxy dials to reach the stub.
func (s *Stub) Addr() string { return fmt.Sprintf("127.0.0.1:%d", s.Port) }

// RemoteIPs returns the source IP of every accepted connection, in order.
func (s *Stub) RemoteIPs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.remotes...)
}

// SNIs returns the server name of every TLS handshake that reached the stub.
func (s *Stub) SNIs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.snis...)
}

// Connections is the number of connections the stub accepted.
func (s *Stub) Connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.remotes)
}

type recordingListener struct {
	net.Listener
	s *Stub
}

func (l *recordingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}

	host, _, _ := net.SplitHostPort(c.RemoteAddr().String())

	l.s.mu.Lock()
	idx := l.s.next
	l.s.next++
	l.s.remotes = append(l.s.remotes, host)
	l.s.mu.Unlock()

	return &recConn{Conn: c, index: idx}, nil
}

type recConn struct {
	net.Conn
	index int
}
