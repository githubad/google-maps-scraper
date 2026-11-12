package gmaps

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/gosom/scrapemate"
	"github.com/stretchr/testify/require"
)

// AS-8 (unit level, the acceptance test for browser mode): when Chromium cannot
// verify the certificate of the search page, the navigation fails with a
// net::ERR_CERT_* error and the search ends blocked with tls_cert - never ok,
// never with results.
func TestBrowser_CertificateErrorIsBlockedTLSCert(t *testing.T) {
	t.Parallel()

	for _, code := range []string{
		"net::ERR_CERT_AUTHORITY_INVALID",
		"net::ERR_CERT_COMMON_NAME_INVALID",
		"net::ERR_CERT_DATE_INVALID",
		"net::ERR_CERT_REVOKED",
		"net::ERR_CERT_CONTAINS_ERRORS",
	} {
		t.Run(code, func(t *testing.T) {
			t.Parallel()

			env := newTrackerEnv(t)
			job := newBrowserJob(t, env)
			page := &fakePage{navErr: errors.New("page.goto: " + code + " at https://www.google.com/maps/search/roofer")}

			resp := job.BrowserActions(shortCtx(t), page)
			require.Error(t, resp.Error)

			res, next, err := job.Process(context.Background(), &resp)
			require.NoError(t, err)
			require.Nil(t, res)
			require.Empty(t, next, "no place jobs after a certificate failure")

			line := env.lines(t)[0]
			require.Equal(t, "blocked", line["status"])
			require.Equal(t, "tls_cert", line["block_reason"])
			require.Nil(t, line["error"])
			require.EqualValues(t, 0, line["results"])
			require.NotEqual(t, "ok", line["status"])
		})
	}
}

func TestBrowser_OtherNavigationErrorsAreNotTLSCert(t *testing.T) {
	t.Parallel()

	for _, e := range []error{
		errors.New("page.goto: net::ERR_SSL_PROTOCOL_ERROR at https://www.google.com/"),
		errors.New("page.goto: Timeout 30000ms exceeded"),
		errors.New("page.goto: net::ERR_PROXY_CONNECTION_FAILED at https://www.google.com/"),
		io.ErrUnexpectedEOF,
	} {
		env := newTrackerEnv(t)
		job := newBrowserJob(t, env)
		page := &fakePage{navErr: e}

		resp := job.BrowserActions(shortCtx(t), page)

		_, _, err := job.Process(context.Background(), &resp)
		require.NoError(t, err)

		line := env.lines(t)[0]
		require.Equal(t, "error", line["status"], "%v", e)
		require.Nil(t, line["block_reason"], "%v", e)
	}
}

// FR-014 for browser tasks: a certificate failure is accepted at once (not
// retried on the same session); everything else keeps the default rule (2xx).
func TestBrowser_CheckResponseDoesNotRetryTLSVerification(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	job := newBrowserJob(t, env)

	require.True(t, job.DoCheckResponse(&scrapemate.Response{
		Error: errors.New("page.goto: net::ERR_CERT_AUTHORITY_INVALID at https://www.google.com/"),
	}))
	require.True(t, job.DoCheckResponse(&scrapemate.Response{Error: tlsFailure(scrapemate.TLSClassExpired)}))

	// default behaviour kept: 2xx accepted, other failures not
	require.True(t, job.DoCheckResponse(&scrapemate.Response{StatusCode: 200}))
	require.False(t, job.DoCheckResponse(&scrapemate.Response{StatusCode: 0, Error: io.ErrUnexpectedEOF}))
	require.False(t, job.DoCheckResponse(&scrapemate.Response{StatusCode: 429}))
}

// Without a tracker (stock mode) the job is exactly upstream's.
func TestBrowser_WithoutTrackerKeepsDefaultCheckResponse(t *testing.T) {
	t.Parallel()

	job := NewGmapJob("t_b", "en", "roofer", 1, false, "30.2,-97.7", 14)

	require.False(t, job.DoCheckResponse(&scrapemate.Response{
		Error: errors.New("page.goto: net::ERR_CERT_AUTHORITY_INVALID at https://www.google.com/"),
	}), "stock behaviour: a response without a 2xx status is not accepted")
}
