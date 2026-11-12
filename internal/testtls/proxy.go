package testtls

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ProxySourceIP is the address the proxy dials the stub from. Loopback is a whole
// /8 on Linux, so a direct connection from the engine (127.0.0.1) is told apart
// from one that went through the proxy.
const ProxySourceIP = "127.0.0.2"

// ProxyMode says how the proxy answers a CONNECT.
type ProxyMode int

// Proxy modes.
const (
	// ProxyForward tunnels to the stub (the default).
	ProxyForward ProxyMode = iota
	// ProxyRefuse answers every CONNECT with Proxy.Status (403, 407, 502 ...).
	ProxyRefuse
	// ProxyReset accepts the CONNECT, then closes the tunnel at once (a reset).
	ProxyReset
	// ProxyFailDial answers 502 as if the upstream could not be reached.
	ProxyFailDial
)

// Proxy is a local HTTP CONNECT proxy. It maps the test host names to the stub,
// dials it from ProxySourceIP and records every CONNECT target and client.
type Proxy struct {
	Mode   ProxyMode
	Status int // for ProxyRefuse

	ln       net.Listener
	hosts    map[string]string
	mu       sync.Mutex
	connects []string
	clients  []string
}

// NewProxy starts a proxy on 127.0.0.1; hosts maps a host name (lower case) to the
// address of the stub. It is closed with the test.
func NewProxy(tb testing.TB, hosts map[string]string) *Proxy {
	tb.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(tb, err)

	p := &Proxy{ln: ln, hosts: hosts, Status: http.StatusForbidden}

	go p.serve()

	tb.Cleanup(func() { _ = ln.Close() })

	return p
}

// URL is the proxy URL for the engine's proxy list.
func (p *Proxy) URL() string { return "http://" + p.ln.Addr().String() }

// SetMode changes how the proxy answers from now on.
func (p *Proxy) SetMode(m ProxyMode) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.Mode = m
}

// ConnectTargets returns the host:port of every CONNECT the proxy received.
func (p *Proxy) ConnectTargets() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.connects...)
}

// Clients returns the source IP of every CONNECT the proxy received.
func (p *Proxy) Clients() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.clients...)
}

func (p *Proxy) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}

		go p.handle(c)
	}
}

func (p *Proxy) handle(c net.Conn) {
	defer c.Close()

	br := bufio.NewReader(c)

	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}

	if req.Method != http.MethodConnect {
		_, _ = io.WriteString(c, "HTTP/1.1 405 Method Not Allowed\r\n\r\n")

		return
	}

	client, _, _ := net.SplitHostPort(c.RemoteAddr().String())

	p.mu.Lock()
	p.connects = append(p.connects, req.Host)
	p.clients = append(p.clients, client)
	mode, status := p.Mode, p.Status
	p.mu.Unlock()

	switch mode {
	case ProxyRefuse:
		_, _ = fmt.Fprintf(c, "HTTP/1.1 %d %s\r\n\r\n", status, http.StatusText(status))

		return
	case ProxyFailDial:
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")

		return
	case ProxyReset:
		_, _ = io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")

		return
	case ProxyForward:
	}

	name := req.Host
	if h, _, splitErr := net.SplitHostPort(req.Host); splitErr == nil {
		name = h
	}

	target, ok := p.hosts[strings.ToLower(name)]
	if !ok {
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")

		return
	}

	d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(ProxySourceIP)}, Timeout: 5 * time.Second}

	up, err := d.Dial("tcp", target)
	if err != nil {
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")

		return
	}
	defer up.Close()

	_, _ = io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")

	done := make(chan struct{}, 2)

	go func() { _, _ = io.Copy(up, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()

	<-done
}

// DirectDials returns the entries of remotes that are not the proxy's address:
// connections to the stub that did not come through the proxy.
func DirectDials(remotes []string) []string {
	var direct []string

	for _, r := range remotes {
		if r != ProxySourceIP {
			direct = append(direct, r)
		}
	}

	return direct
}

// AssertNoDirectDial fails tb when the stub saw a connection that did not come
// from the proxy.
func AssertNoDirectDial(tb testing.TB, s *Stub) {
	tb.Helper()

	require.Empty(tb, DirectDials(s.RemoteIPs()), "the stub saw a connection that did not come from the proxy")
}
