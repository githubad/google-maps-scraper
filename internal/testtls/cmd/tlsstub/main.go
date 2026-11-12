// Command tlsstub is a test-only HTTPS server standing in for Google, for the
// docker tests of gmx (an engine container on an internal network). It generates
// its own certificate authority, which nothing trusts, so a verifying client must
// refuse it. Every accepted connection is logged to stdout as "conn <ip>".
//
//	tlsstub -addr :8443 -host maps.test[,other.test] [-fixture page.json] [-mode untrusted|expired|wronghost]
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/internal/testtls"
)

type loggingListener struct{ net.Listener }

func (l loggingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		host, _, _ := net.SplitHostPort(c.RemoteAddr().String())
		fmt.Fprintf(os.Stdout, "conn %s\n", host)
	}

	return c, err
}

func main() {
	addr := flag.String("addr", ":8443", "listen address")
	hosts := flag.String("host", "maps.test", "comma separated host names the certificate is valid for")
	fixture := flag.String("fixture", "", "file served for every request (a list-mode page); a small HTML page when empty")
	mode := flag.String("mode", "untrusted", "untrusted (the default: a certificate from an unknown CA), expired, or wronghost")

	flag.Parse()

	body := []byte("<html><head><title>stub</title></head><body>stub</body></html>")

	if *fixture != "" {
		b, err := os.ReadFile(*fixture)
		if err != nil {
			log.Fatal(err)
		}

		body = b
	}

	ca := testtls.MustCA("tlsstub untrusted CA")

	names := strings.Split(*hosts, ",")
	notBefore, notAfter := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)

	switch *mode {
	case "expired":
		notBefore, notAfter = time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour)
	case "wronghost":
		names = []string{"not-" + names[0]}
	case "untrusted":
	default:
		log.Fatalf("unknown mode %q", *mode)
	}

	cert, err := ca.IssueCert(names, notBefore, notAfter)
	if err != nil {
		log.Fatal(err)
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Fprintf(os.Stdout, "listening %s\n", ln.Addr())

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }),
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{*cert},
			NextProtos:   []string{"http/1.1"},
		},
		ErrorLog:          log.New(io.Discard, "", 0),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Fatal(srv.ServeTLS(loggingListener{ln}, "", ""))
}
