package testtls_test

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gosom/google-maps-scraper/internal/testtls"
)

func poolOf(ca *testtls.CA) *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.Cert)

	return p
}

func leafOf(t *testing.T, c *tls.Certificate) *x509.Certificate {
	t.Helper()

	leaf, err := x509.ParseCertificate(c.Certificate[0])
	require.NoError(t, err)

	return leaf
}

func TestCA_CertificatesVerifyAsDesigned(t *testing.T) {
	t.Parallel()

	ca := testtls.NewCA(t, "test ca")
	other := testtls.NewCA(t, "other ca")

	verify := func(c *tls.Certificate, host string, roots *x509.CertPool) error {
		_, err := leafOf(t, c).Verify(x509.VerifyOptions{DNSName: host, Roots: roots, CurrentTime: time.Now()})

		return err
	}

	require.NoError(t, verify(ca.Valid(t, "maps.test"), "maps.test", poolOf(ca)))
	require.Error(t, verify(ca.Valid(t, "maps.test"), "maps.test", poolOf(other)), "a different CA does not verify")
	require.Error(t, verify(ca.Valid(t, "elsewhere.test"), "maps.test", poolOf(ca)), "wrong host name")
	require.Error(t, verify(ca.Expired(t, "maps.test"), "maps.test", poolOf(ca)), "expired")
	require.Error(t, verify(ca.NotYetValid(t, "maps.test"), "maps.test", poolOf(ca)), "not yet valid")
}

func TestCA_WriteBundle(t *testing.T) {
	t.Parallel()

	ca := testtls.NewCA(t, "test ca")
	path := ca.WriteBundle(t, t.TempDir())

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(data))
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "hello") })
}

// connectThrough opens a CONNECT tunnel to host through the proxy and returns the
// connection and the proxy's status line.
func connectThrough(t *testing.T, proxyURL, host string) (conn net.Conn, status string) {
	t.Helper()

	c, err := net.Dial("tcp", proxyURL[len("http://"):])
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	_, err = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", host, host)
	require.NoError(t, err)

	status, err = bufio.NewReader(c).ReadString('\n')
	require.NoError(t, err)

	return c, status
}

func TestStubAndProxy_TheStubSeesTheProxyNotTheClient(t *testing.T) {
	t.Parallel()

	ca := testtls.NewCA(t, "test ca")
	stub := testtls.NewStub(t, testtls.Always(ca.Valid(t, "maps.test")), okHandler())
	proxy := testtls.NewProxy(t, map[string]string{"maps.test": stub.Addr()})

	conn, status := connectThrough(t, proxy.URL(), "maps.test:443")
	require.Contains(t, status, "200")

	tc := tls.Client(conn, &tls.Config{ServerName: "maps.test", RootCAs: poolOf(ca)})
	require.NoError(t, tc.Handshake())

	require.Equal(t, []string{"maps.test:443"}, proxy.ConnectTargets())
	require.Equal(t, []string{"127.0.0.1"}, proxy.Clients())
	require.Equal(t, []string{testtls.ProxySourceIP}, stub.RemoteIPs())
	require.Equal(t, []string{"maps.test"}, stub.SNIs())
	testtls.AssertNoDirectDial(t, stub)
}

func TestStub_ADirectConnectionIsVisibleAsDirect(t *testing.T) {
	t.Parallel()

	ca := testtls.NewCA(t, "test ca")
	stub := testtls.NewStub(t, testtls.Always(ca.Valid(t, "maps.test")), okHandler())

	c, err := net.Dial("tcp", stub.Addr())
	require.NoError(t, err)

	defer c.Close()

	tc := tls.Client(c, &tls.Config{ServerName: "maps.test", RootCAs: poolOf(ca)})
	require.NoError(t, tc.Handshake())

	require.Equal(t, []string{"127.0.0.1"}, testtls.DirectDials(stub.RemoteIPs()))
	require.Equal(t, 1, stub.Connections())
}

func TestStub_ChooserSeesTheConnectionIndexAndSNI(t *testing.T) {
	t.Parallel()

	ca := testtls.NewCA(t, "test ca")
	rogue := testtls.NewCA(t, "rogue")

	var seen []string

	stub := testtls.NewStub(t, func(idx int, sni string) *tls.Certificate {
		seen = append(seen, fmt.Sprintf("%d:%s", idx, sni))

		if idx == 0 {
			return ca.Valid(t, "maps.test")
		}

		return rogue.Valid(t, "maps.test")
	}, okHandler())

	handshake := func(roots *x509.CertPool) error {
		c, err := net.Dial("tcp", stub.Addr())
		require.NoError(t, err)

		defer c.Close()

		return tls.Client(c, &tls.Config{ServerName: "maps.test", RootCAs: roots}).Handshake()
	}

	require.NoError(t, handshake(poolOf(ca)))
	require.Error(t, handshake(poolOf(ca)), "the second connection gets the rogue certificate")
	require.Equal(t, []string{"0:maps.test", "1:maps.test"}, seen)
}

func TestProxy_Modes(t *testing.T) {
	t.Parallel()

	ca := testtls.NewCA(t, "test ca")
	stub := testtls.NewStub(t, testtls.Always(ca.Valid(t, "maps.test")), okHandler())
	proxy := testtls.NewProxy(t, map[string]string{"maps.test": stub.Addr()})

	proxy.SetMode(testtls.ProxyRefuse)
	proxy.Status = http.StatusProxyAuthRequired

	_, status := connectThrough(t, proxy.URL(), "maps.test:443")
	require.Contains(t, status, "407")

	proxy.SetMode(testtls.ProxyFailDial)

	_, status = connectThrough(t, proxy.URL(), "maps.test:443")
	require.Contains(t, status, "502")

	proxy.SetMode(testtls.ProxyReset)

	conn, status := connectThrough(t, proxy.URL(), "maps.test:443")
	require.Contains(t, status, "200")
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))

	_, err := conn.Read(make([]byte, 1))
	require.Error(t, err, "the tunnel is closed straight away")

	proxy.SetMode(testtls.ProxyForward)

	_, status = connectThrough(t, proxy.URL(), "unknown.test:443")
	require.Contains(t, status, "502", "a host the proxy does not know")
	require.Zero(t, stub.Connections(), "none of these reached the stub")
}
