package filerunner_test

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gosom/google-maps-scraper/internal/testtls"
	"github.com/gosom/google-maps-scraper/runner"
	"github.com/gosom/google-maps-scraper/runner/filerunner"
)

// These tests run the real runner, scrapemate and the stealth fetcher against a
// local HTTPS stub standing in for Google, reached only through a local CONNECT
// proxy (docs/contracts.md §1 TLS guarantee, §4.2 tls_cert, §5 raw note). All
// certificates are generated at test time; nothing leaves the machine.

// trustedCA is the only authority in the process's trust store: TestMain points
// SSL_CERT_FILE at a bundle that holds just this certificate. Go loads the system
// roots once per process, so this must happen before the first verifying fetch.
var trustedCA = testtls.MustCA("gmx test trusted CA")

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gmx-tls-ca-")
	if err != nil {
		panic(err)
	}

	bundle, err := trustedCA.WriteBundleFile(dir)
	if err != nil {
		panic(err)
	}

	if err := os.Setenv("SSL_CERT_FILE", bundle); err != nil {
		panic(err)
	}

	code := m.Run()

	_ = os.RemoveAll(dir)

	os.Exit(code)
}

const (
	hostMaps    = "maps.test"
	hostConsent = "consent.test"
)

type tlsRig struct {
	stub  *testtls.Stub
	proxy *testtls.Proxy
}

func newTLSRig(t *testing.T, chooser testtls.Chooser, handler http.Handler) *tlsRig {
	t.Helper()

	stub := testtls.NewStub(t, chooser, handler)
	proxy := testtls.NewProxy(t, map[string]string{
		hostMaps:    stub.Addr(),
		hostConsent: stub.Addr(),
	})

	return &tlsRig{stub: stub, proxy: proxy}
}

// run drives the file runner with the stub as the maps endpoint and the proxy as
// the only way out.
func (r *tlsRig) run(t *testing.T, host string, taskLines []string, mutate func(*runner.Config)) *batchResult {
	t.Helper()
	t.Setenv("DISABLE_TELEMETRY", "1")

	dir := t.TempDir()
	input := filepath.Join(dir, "tasks.jsonl")
	require.NoError(t, os.WriteFile(input, []byte(strings.Join(taskLines, "\n")+"\n"), 0o600))

	cfg := testConfig(input, filepath.Join(dir, "results.jsonl"))
	cfg.InputFormat = "jsonl"
	cfg.JSON = true
	cfg.MapsBaseURL = fmt.Sprintf("https://%s:%d/search", host, r.stub.Port)
	cfg.Radius = 0
	cfg.RawDir = filepath.Join(dir, "raw")
	cfg.SearchReport = filepath.Join(dir, "searches.jsonl")
	cfg.Proxies = []string{r.proxy.URL()}

	if mutate != nil {
		mutate(cfg)
	}

	res := batchResult{dir: dir, rawDir: cfg.RawDir, report: cfg.SearchReport}

	fr, err := filerunner.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = fr.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	res.err = fr.Run(ctx)
	if errors.Is(res.err, context.Canceled) {
		res.err = nil
	}

	res.hits = int64(r.stub.Connections())
	res.lines = readReportLines(t, cfg.SearchReport)

	return &res
}

// onePageTask is the task "t_1", which asks for one page only.
func onePageTask() string {
	return `{"id":"t_1","query":"roofer","lat":30.2672,"lng":-97.7431,"zoom":14,"max_pages":1}`
}

// pages serves the first fixture page at offset 0 and an altered copy at offset 20.
func pages(t *testing.T) http.Handler {
	t.Helper()

	p0 := readFixture(t, "har_01_roofing-contractor_o0.json")
	p1 := alterPage(t, readFixture(t, "har_02_roofing-contractor_o20.json"), "x")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch pageOffset(r) {
		case 0:
			_, _ = w.Write(p0)
		case 20:
			_, _ = w.Write(p1)
		default:
			http.NotFound(w, r)
		}
	})
}

func readGz(t *testing.T, path string) string {
	t.Helper()

	f, err := os.Open(path)
	require.NoError(t, err)

	defer f.Close()

	zr, err := gzip.NewReader(f)
	require.NoError(t, err)

	b, err := io.ReadAll(zr)
	require.NoError(t, err)

	return string(b)
}

func rawFiles(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}

	require.NoError(t, err)

	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}

	return names
}

// resultRecords counts the result records in the results file. The stock JSON
// writer writes a bare `null` line for a task that produced no rows; that is not
// a record.
func resultRecords(t *testing.T, dir string) int {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(dir, "results.jsonl"))
	if os.IsNotExist(err) {
		return 0
	}

	require.NoError(t, err)

	n := 0

	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || l == "null" {
			continue
		}

		require.True(t, strings.HasPrefix(l, "{"), "unexpected line in the results file: %q", l)

		n++
	}

	return n
}

// requireTLSBlocked asserts the report line of a search that failed verification
// on page 0: blocked, tls_cert, nothing stored, a raw note and no body.
func requireTLSBlocked(t *testing.T, res *batchResult, class string) {
	t.Helper()

	require.NoError(t, res.err)
	require.Len(t, res.lines, 1)

	line := res.lines[0]
	require.Equal(t, "blocked", line["status"])
	require.Equal(t, "tls_cert", line["block_reason"])
	require.Nil(t, line["error"])
	require.EqualValues(t, 0, line["results"])
	require.EqualValues(t, 0, line["pages_ok"])
	require.Equal(t, []int{0}, intsOf(t, line["http_statuses"]))
	require.EqualValues(t, 0, line["bytes_in"])

	require.Equal(t, []string{"t_1_p0.json.gz"}, rawFiles(t, res.rawDir), "one raw file, and it is the note")

	note := readGz(t, filepath.Join(res.rawDir, "t_1_p0.json.gz"))
	require.True(t, strings.HasPrefix(note, `{"note":"tls_cert"`), note)
	require.Contains(t, note, `"class":"`+class+`"`)
	require.NotContains(t, note, ")]}'", "no response body was written")

	require.Zero(t, resultRecords(t, res.dir), "nothing was ingested")
}

// AS-1: a certificate from an authority the container does not trust.
func TestTLS_AS1_UntrustedCA(t *testing.T) {
	rogue := testtls.NewCA(t, "rogue")
	rig := newTLSRig(t, testtls.Always(rogue.Valid(t, hostMaps)), pages(t))

	res := rig.run(t, hostMaps, []string{onePageTask()}, nil)

	requireTLSBlocked(t, res, "unknown_authority")

	// AS-15: one handshake, no same-session retry
	require.Equal(t, 1, rig.stub.Connections())
	require.Equal(t, []string{fmt.Sprintf("%s:%d", hostMaps, rig.stub.Port)}, rig.proxy.ConnectTargets())

	// AS-7
	testtls.AssertNoDirectDial(t, rig.stub)
}

// AS-2: an authority the container trusts, a certificate for the requested host.
func TestTLS_AS2_TrustedCA(t *testing.T) {
	rig := newTLSRig(t, testtls.Always(trustedCA.Valid(t, hostMaps, hostConsent)), pages(t))

	res := rig.run(t, hostMaps, []string{onePageTask()}, nil)

	require.NoError(t, res.err)
	require.Len(t, res.lines, 1)

	line := res.lines[0]
	require.Equal(t, "ok", line["status"])
	require.Nil(t, line["block_reason"])
	require.EqualValues(t, 20, line["results"])
	require.EqualValues(t, 20, line["rows_seen"])
	require.Equal(t, []int{200}, intsOf(t, line["http_statuses"]))

	require.Equal(t, []string{"t_1_p0.json.gz"}, rawFiles(t, res.rawDir))
	require.True(t, strings.HasPrefix(readGz(t, filepath.Join(res.rawDir, "t_1_p0.json.gz")), ")]}'"), "a page, not a note")

	// AS-7: every connection came through the proxy, to the requested host only
	testtls.AssertNoDirectDial(t, rig.stub)
	require.Equal(t, []string{fmt.Sprintf("%s:%d", hostMaps, rig.stub.Port)}, rig.proxy.ConnectTargets())
	require.Equal(t, []string{hostMaps}, rig.stub.SNIs(), "SNI is the URL host")
}

// AS-3: a trusted authority, a certificate for another name.
func TestTLS_AS3_WrongHostname(t *testing.T) {
	rig := newTLSRig(t, testtls.Always(trustedCA.Valid(t, "other.test")), pages(t))

	res := rig.run(t, hostMaps, []string{onePageTask()}, nil)

	requireTLSBlocked(t, res, "hostname_mismatch")
	testtls.AssertNoDirectDial(t, rig.stub)
}

// AS-4: an expired certificate (and, beside it, one that is not valid yet).
func TestTLS_AS4_ExpiredAndNotYetValid(t *testing.T) {
	t.Run("expired", func(t *testing.T) {
		rig := newTLSRig(t, testtls.Always(trustedCA.Expired(t, hostMaps)), pages(t))

		requireTLSBlocked(t, rig.run(t, hostMaps, []string{onePageTask()}, nil), "expired")
		testtls.AssertNoDirectDial(t, rig.stub)
	})

	t.Run("not yet valid", func(t *testing.T) {
		rig := newTLSRig(t, testtls.Always(trustedCA.NotYetValid(t, hostMaps)), pages(t))

		requireTLSBlocked(t, rig.run(t, hostMaps, []string{onePageTask()}, nil), "not_yet_valid")
	})
}

// AS-5: page 0 verifies, page 1 does not -> partial with tls_cert, page 0 rows kept.
func TestTLS_AS5_LaterPageUntrusted(t *testing.T) {
	rogue := testtls.NewCA(t, "rogue")
	good := trustedCA.Valid(t, hostMaps)
	bad := rogue.Valid(t, hostMaps)

	rig := newTLSRig(t, func(index int, _ string) *tls.Certificate {
		if index == 0 {
			return good
		}

		return bad
	}, pages(t))

	res := rig.run(t, hostMaps, []string{taskLine("t_1", "roofer")}, nil)

	require.NoError(t, res.err)
	require.Len(t, res.lines, 1)

	line := res.lines[0]
	require.Equal(t, "partial", line["status"])
	require.Equal(t, "tls_cert", line["block_reason"])
	require.EqualValues(t, 20, line["results"], "the rows of page 0 are kept")
	require.EqualValues(t, 1, line["pages_ok"])
	require.Equal(t, []int{200, 0}, intsOf(t, line["http_statuses"]))

	require.ElementsMatch(t, []string{"t_1_p0.json.gz", "t_1_p1.json.gz"}, rawFiles(t, res.rawDir))
	require.True(t, strings.HasPrefix(readGz(t, filepath.Join(res.rawDir, "t_1_p0.json.gz")), ")]}'"))
	require.True(t, strings.HasPrefix(readGz(t, filepath.Join(res.rawDir, "t_1_p1.json.gz")), `{"note":"tls_cert"`))

	require.Equal(t, 20, resultRecords(t, res.dir), "page 0 rows were written")
	require.Equal(t, 2, rig.stub.Connections(), "no retry of the failed handshake")
	testtls.AssertNoDirectDial(t, rig.stub)
}

// AS-6: a valid trusted certificate, but the body is a router's HTML page: the
// classification order is unchanged.
func TestTLS_AS6_HTMLOverAGoodHandshake(t *testing.T) {
	html := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<html><head><title>eero - Device Paused</title></head></html>")
	})
	rig := newTLSRig(t, testtls.Always(trustedCA.Valid(t, hostMaps)), html)

	res := rig.run(t, hostMaps, []string{onePageTask()}, nil)

	require.NoError(t, res.err)
	require.Len(t, res.lines, 1)

	line := res.lines[0]
	require.Equal(t, "blocked", line["status"])
	require.Equal(t, "no_xssi_prefix", line["block_reason"])
	require.Equal(t, []int{200}, intsOf(t, line["http_statuses"]))
	testtls.AssertNoDirectDial(t, rig.stub)
}

// AS-15 and AS-17: other failures are not tls_cert, and the network-failure
// retries (two, so three connection attempts) are unchanged.
func TestTLS_AS15_AS17_ProxyFailuresAreNotTLSCert(t *testing.T) {
	tests := []struct {
		name     string
		mode     testtls.ProxyMode
		status   int
		attempts int
	}{
		{"connection reset after CONNECT", testtls.ProxyReset, 0, 3},
		{"CONNECT refused (403)", testtls.ProxyRefuse, http.StatusForbidden, 3},
		{"proxy authentication required (407)", testtls.ProxyRefuse, http.StatusProxyAuthRequired, 3},
		{"bad gateway (dial failed)", testtls.ProxyFailDial, 0, 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := newTLSRig(t, testtls.Always(trustedCA.Valid(t, hostMaps)), pages(t))
			rig.proxy.SetMode(tc.mode)

			if tc.status != 0 {
				rig.proxy.Status = tc.status
			}

			res := rig.run(t, hostMaps, []string{onePageTask()}, nil)

			require.NoError(t, res.err)
			require.Len(t, res.lines, 1)

			line := res.lines[0]
			require.Equal(t, "error", line["status"])
			require.Nil(t, line["block_reason"], "a proxy failure is not a block, and never tls_cert")
			require.Equal(t, []int{0}, intsOf(t, line["http_statuses"]))
			require.GreaterOrEqual(t, len(rig.proxy.ConnectTargets()), tc.attempts,
				"the first try and the two network-failure retries on the same session")
			require.Zero(t, rig.stub.Connections(), "the failure was in the proxy; nothing reached the stub")
			testtls.AssertNoDirectDial(t, rig.stub)
		})
	}
}

// AS-20: a redirect to another host (a stand-in for consent.google.com).
func TestTLS_AS20_Redirects(t *testing.T) {
	// maps.test redirects to consent.test, which serves the page
	handler := func(t *testing.T) http.Handler {
		t.Helper()

		page := pages(t)

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.Host, hostMaps) {
				target := "https://" + hostConsent + r.Host[len(hostMaps):] + r.URL.RequestURI()
				http.Redirect(w, r, target, http.StatusFound) //nolint:gosec // test stub; the target is the fixed second test host

				return
			}

			page.ServeHTTP(w, r)
		})
	}

	t.Run("good certificate on the second host", func(t *testing.T) {
		rig := newTLSRig(t, testtls.Always(trustedCA.Valid(t, hostMaps, hostConsent)), handler(t))

		res := rig.run(t, hostMaps, []string{onePageTask()}, nil)

		require.NoError(t, res.err)
		require.Len(t, res.lines, 1)
		require.Equal(t, "ok", res.lines[0]["status"])
		require.EqualValues(t, 20, res.lines[0]["results"])
		require.Equal(t, []string{
			fmt.Sprintf("%s:%d", hostMaps, rig.stub.Port),
			fmt.Sprintf("%s:%d", hostConsent, rig.stub.Port),
		}, rig.proxy.ConnectTargets(), "both hosts were reached through the proxy")
		testtls.AssertNoDirectDial(t, rig.stub)
	})

	t.Run("untrusted certificate on the second host", func(t *testing.T) {
		rogue := testtls.NewCA(t, "rogue")
		good := trustedCA.Valid(t, hostMaps)
		bad := rogue.Valid(t, hostConsent)

		rig := newTLSRig(t, func(_ int, sni string) *tls.Certificate {
			if sni == hostConsent {
				return bad
			}

			return good
		}, handler(t))

		requireTLSBlocked(t, rig.run(t, hostMaps, []string{onePageTask()}, nil), "unknown_authority")
	})

	t.Run("certificate for another name on the second host", func(t *testing.T) {
		good := trustedCA.Valid(t, hostMaps)
		wrong := trustedCA.Valid(t, "elsewhere.test")

		rig := newTLSRig(t, func(_ int, sni string) *tls.Certificate {
			if sni == hostConsent {
				return wrong
			}

			return good
		}, handler(t))

		requireTLSBlocked(t, rig.run(t, hostMaps, []string{onePageTask()}, nil), "hostname_mismatch")
	})
}

// AS-7, mutation check. The no-direct-dial assertions above are only worth
// something if they can fail. Here the engine is run with no proxy at all, so its
// connection goes straight to the stub: the stub then sees the engine's own
// address (127.0.0.1) instead of the proxy's (127.0.0.2), and DirectDials, the
// function AssertNoDirectDial is built on, reports it. (The scrapemate fork has a
// second mutation test that uses the library's own pin path, which dials the
// host directly even when a proxy is configured.)
func TestTLS_AS7_MutationTheCheckSeesADirectDial(t *testing.T) {
	rig := newTLSRig(t, testtls.Always(trustedCA.Valid(t, "localhost")), pages(t))

	res := rig.run(t, "localhost", []string{onePageTask()}, func(cfg *runner.Config) {
		cfg.Proxies = nil // the deliberate direct dial
	})

	require.NoError(t, res.err)
	require.Len(t, res.lines, 1)
	require.Equal(t, "ok", res.lines[0]["status"], "the page itself verifies fine; only the route differs")

	direct := testtls.DirectDials(rig.stub.RemoteIPs())
	require.NotEmpty(t, direct, "without the proxy the stub must see the engine's own address")
	require.NotContains(t, direct, testtls.ProxySourceIP)
	require.Empty(t, rig.proxy.ConnectTargets(), "and the proxy saw nothing")
}
