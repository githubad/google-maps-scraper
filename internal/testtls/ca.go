// Package testtls is a test harness for TLS verification without any network: a
// throw-away certificate authority, an HTTPS stub that stands in for Google and a
// CONNECT proxy that records who connects to whom. Everything is generated at test
// time and kept in memory; no key is ever written to the repository.
package testtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// CA is a certificate authority generated for one test.
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
}

// NewCA creates a CA with the given common name.
func NewCA(tb testing.TB, name string) *CA {
	tb.Helper()

	c, err := newCA(name)
	require.NoError(tb, err)

	return c
}

// MustCA is NewCA for TestMain, where there is no testing.TB; it panics on error.
func MustCA(name string) *CA {
	c, err := newCA(name)
	if err != nil {
		panic(err)
	}

	return c
}

func newCA(name string) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}

	return &CA{Cert: cert, Key: key}, nil
}

var serialCounter atomic.Int64

func serial() *big.Int {
	return big.NewInt(time.Now().UnixNano() + serialCounter.Add(1))
}

// Issue returns a server certificate for names, valid between notBefore and notAfter.
func (c *CA) Issue(tb testing.TB, names []string, notBefore, notAfter time.Time) *tls.Certificate {
	tb.Helper()

	cert, err := c.IssueCert(names, notBefore, notAfter)
	require.NoError(tb, err)

	return cert
}

// IssueCert is Issue without a testing.TB, for the helper programs under cmd/.
func (c *CA) IssueCert(names []string, notBefore, notAfter time.Time) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: names[0]},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		DNSNames:     names,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.Cert, &key.PublicKey, c.Key)
	if err != nil {
		return nil, err
	}

	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// Valid returns a certificate for names that is valid now.
func (c *CA) Valid(tb testing.TB, names ...string) *tls.Certificate {
	tb.Helper()

	return c.Issue(tb, names, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
}

// Expired returns a certificate for names whose validity ended an hour ago.
func (c *CA) Expired(tb testing.TB, names ...string) *tls.Certificate {
	tb.Helper()

	return c.Issue(tb, names, time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour))
}

// NotYetValid returns a certificate for names whose validity starts in an hour.
func (c *CA) NotYetValid(tb testing.TB, names ...string) *tls.Certificate {
	tb.Helper()

	return c.Issue(tb, names, time.Now().Add(time.Hour), time.Now().Add(48*time.Hour))
}

// WriteBundleFile writes the CA certificate as a PEM file in dir and returns its
// path, for SSL_CERT_FILE.
func (c *CA) WriteBundleFile(dir string) (string, error) {
	path := filepath.Join(dir, "ca-bundle.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Cert.Raw})

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}

	return path, nil
}

// WriteBundle is WriteBundleFile for a test.
func (c *CA) WriteBundle(tb testing.TB, dir string) string {
	tb.Helper()

	path, err := c.WriteBundleFile(dir)
	require.NoError(tb, err)

	return path
}
