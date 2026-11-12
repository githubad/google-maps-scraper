package gmaps

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gosom/scrapemate"
	"github.com/stretchr/testify/require"
)

func tlsFailure(class string) error {
	return &scrapemate.TLSVerifyError{
		Host:  "maps.google.com",
		Class: class,
		Err:   errors.New("x509: certificate signed by unknown authority"),
	}
}

// AS-1 (unit level): a certificate that does not verify on page 0 is a blocked
// search with block_reason tls_cert, no results, status 0 in http_statuses and a
// raw note instead of a body.
func TestSearchJobTracker_TLSFailureOnPageZeroIsBlocked(t *testing.T) {
	t.Parallel()

	rawDir := t.TempDir()
	env := newTrackerEnv(t)
	exit := &recordingExiter{}

	job := trackedJob(t, "http://127.0.0.1:1/search", 6, env, WithSearchJobRawDir(rawDir), WithSearchJobExitMonitor(exit))

	res, children, err := job.Process(context.Background(), &scrapemate.Response{Error: tlsFailure(scrapemate.TLSClassUnknownAuthority)})

	require.NoError(t, err)
	require.Empty(t, children, "no next page after a certificate failure")
	require.Empty(t, flattenResult(res))
	require.Equal(t, 1, exit.seedsCompleted)

	line := env.lines(t)[0]
	require.Equal(t, "blocked", line["status"])
	require.Equal(t, "tls_cert", line["block_reason"])
	require.Nil(t, line["error"], "a block carries its cause in block_reason")
	require.EqualValues(t, 0, line["results"])
	require.EqualValues(t, 1, line["pages_requested"])
	require.EqualValues(t, 0, line["pages_ok"])
	require.Equal(t, []any{float64(0)}, line["http_statuses"])
	require.EqualValues(t, 0, line["bytes_in"])

	// a note instead of a body, and nothing else in the raw folder
	entries, readErr := os.ReadDir(rawDir)
	require.NoError(t, readErr)
	require.Len(t, entries, 1)

	note := string(readGzipFile(t, filepath.Join(rawDir, "t_x_p0.json.gz")))
	require.Contains(t, note, `"note":"tls_cert"`)
	require.Contains(t, note, `"class":"unknown_authority"`)
	require.Contains(t, note, `"host":"maps.google.com"`)
}

// A body that somehow came with a failed handshake is never written or parsed.
func TestSearchJobTracker_TLSFailureIgnoresAnyBody(t *testing.T) {
	t.Parallel()

	rawDir := t.TempDir()
	env := newTrackerEnv(t)
	job := trackedJob(t, "http://127.0.0.1:1/search", 6, env, WithSearchJobRawDir(rawDir))

	_, _, err := job.Process(context.Background(), &scrapemate.Response{
		Error: tlsFailure(scrapemate.TLSClassHostnameMismatch),
		Body:  append([]byte(")]}'\n"), outputPage(t)...),
	})

	require.NoError(t, err)

	line := env.lines(t)[0]
	require.Equal(t, "blocked", line["status"])
	require.EqualValues(t, 0, line["results"])
	require.EqualValues(t, 0, line["bytes_in"])

	note := string(readGzipFile(t, filepath.Join(rawDir, "t_x_p0.json.gz")))
	require.Contains(t, note, `"note":"tls_cert"`)
	require.NotContains(t, note, ")]}'")
}

// AS-5 (unit level): page 0 fine, page 1 fails verification -> partial with
// tls_cert, and the rows of page 0 are kept.
func TestSearchJobTracker_TLSFailureOnLaterPageIsPartial(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	full := outputPage(t)
	rawDir := t.TempDir()

	srv := statusServer(t, map[int]reply{0: {status: 200, body: full}})
	job := trackedJob(t, srv.URL, 3, env, WithSearchJobRawDir(rawDir))

	res, children, err := job.Process(context.Background(), fetchJob(t, job))
	require.NoError(t, err)
	require.Len(t, children, 1)

	kept := len(flattenResult(res))
	require.Positive(t, kept)

	next, ok := children[0].(*SearchJob)
	require.True(t, ok)

	_, children, err = next.Process(context.Background(), &scrapemate.Response{Error: tlsFailure(scrapemate.TLSClassExpired)})
	require.NoError(t, err)
	require.Empty(t, children, "no further page after a certificate failure")

	line := env.lines(t)[0]
	require.Equal(t, "partial", line["status"])
	require.Equal(t, "tls_cert", line["block_reason"])
	require.EqualValues(t, kept, line["results"], "page 0 rows are kept")
	require.EqualValues(t, 1, line["pages_ok"])
	require.EqualValues(t, 2, line["pages_requested"])
	require.Equal(t, []any{float64(200), float64(0)}, line["http_statuses"])

	// page 0 has its body file, page 1 has the note
	require.FileExists(t, filepath.Join(rawDir, "t_x_p0.json.gz"))
	require.Contains(t, string(readGzipFile(t, filepath.Join(rawDir, "t_x_p1.json.gz"))), `"note":"tls_cert"`)
}

// A certificate failure takes no polite pause: there was no response.
func TestSearchJobTracker_TLSFailureTakesNoPause(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	job := trackedJob(t, "http://127.0.0.1:1/search", 3, env, WithSearchJobDelay(3*time.Second, 3*time.Second))

	start := time.Now()
	_, _, err := job.Process(context.Background(), &scrapemate.Response{Error: tlsFailure(scrapemate.TLSClassOther)})

	require.NoError(t, err)
	require.Less(t, time.Since(start), time.Second)
}

// A plain network failure stays an error; only a verification failure is tls_cert.
func TestSearchJobTracker_OtherNetworkFailuresAreNotTLSCert(t *testing.T) {
	t.Parallel()

	for _, e := range []error{
		io.ErrUnexpectedEOF,
		errors.New("failed to handshake: remote error: tls: handshake failure"),
		errors.New("net::ERR_SSL_PROTOCOL_ERROR"),
	} {
		env := newTrackerEnv(t)
		job := trackedJob(t, "http://127.0.0.1:1/search", 3, env)

		_, _, err := job.Process(context.Background(), &scrapemate.Response{Error: e})
		require.NoError(t, err)

		line := env.lines(t)[0]
		require.Equal(t, "error", line["status"], "%v", e)
		require.Nil(t, line["block_reason"], "%v", e)
		require.Equal(t, []any{float64(0)}, line["http_statuses"])
	}
}

// AS-6: over a good handshake an HTML page is still no_xssi_prefix.
func TestSearchJobTracker_HTMLOverGoodHandshakeIsStillNoXSSIPrefix(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	job := trackedJob(t, "http://127.0.0.1:1/search", 3, env)

	_, _, err := job.Process(context.Background(), &scrapemate.Response{
		StatusCode: 200,
		Body:       []byte("<html><title>eero - Device Paused</title></html>"),
	})
	require.NoError(t, err)

	line := env.lines(t)[0]
	require.Equal(t, "blocked", line["status"])
	require.Equal(t, "no_xssi_prefix", line["block_reason"])
}

// -max-blocked counts a page-0 certificate failure like any blocked task.
func TestSearchJobTracker_TLSFailureCountsForMaxBlocked(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	fired := 0

	env.tr.SetBlockedThreshold(1, func() { fired++ })

	job := trackedJob(t, "http://127.0.0.1:1/search", 3, env)

	_, _, err := job.Process(context.Background(), &scrapemate.Response{Error: tlsFailure(scrapemate.TLSClassUnknownAuthority)})
	require.NoError(t, err)

	require.Equal(t, 1, fired)
}

// FR-014 / AS-15: the engine does not retry a certificate verification failure
// on the same session, but still retries any other failure without a status.
func TestSearchJobTracker_CheckResponseDoesNotRetryTLSVerification(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	job := trackedJob(t, "http://127.0.0.1:1/search", 3, env)

	for _, class := range []string{
		scrapemate.TLSClassUnknownAuthority,
		scrapemate.TLSClassHostnameMismatch,
		scrapemate.TLSClassExpired,
		scrapemate.TLSClassNotYetValid,
		scrapemate.TLSClassOther,
	} {
		require.True(t, job.DoCheckResponse(&scrapemate.Response{Error: tlsFailure(class)}),
			"%s reaches Process once (accepted, not retried)", class)
	}

	require.True(t, job.DoCheckResponse(&scrapemate.Response{
		Error: errors.New("page.goto: net::ERR_CERT_COMMON_NAME_INVALID at https://x.test/"),
	}))

	require.False(t, job.DoCheckResponse(&scrapemate.Response{Error: io.ErrUnexpectedEOF}), "a reset is still retried")
	require.False(t, job.DoCheckResponse(&scrapemate.Response{Error: errors.New("failed to handshake: i/o timeout")}))
	require.Equal(t, 2, job.GetMaxRetries(), "the network-failure retries are unchanged")
}
