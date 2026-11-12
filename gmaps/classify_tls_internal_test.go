package gmaps

import (
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/gosom/scrapemate"
	"github.com/stretchr/testify/require"
)

func TestReasonTLSCert_IsTheContractString(t *testing.T) {
	t.Parallel()

	require.Equal(t, BlockReason("tls_cert"), ReasonTLSCert)
}

func TestClassifyFetchError(t *testing.T) {
	t.Parallel()

	verifyErr := func(class string) error {
		return &scrapemate.TLSVerifyError{Host: "maps.google.com", Class: class, Err: errors.New("x509: bad")}
	}

	for _, class := range []string{
		scrapemate.TLSClassUnknownAuthority,
		scrapemate.TLSClassHostnameMismatch,
		scrapemate.TLSClassExpired,
		scrapemate.TLSClassNotYetValid,
		scrapemate.TLSClassDateInvalid,
		scrapemate.TLSClassOther,
	} {
		t.Run(class, func(t *testing.T) {
			t.Parallel()

			outcome, reason, host, gotClass, ok := classifyFetchError(verifyErr(class))

			require.True(t, ok)
			require.Equal(t, PageBlocked, outcome)
			require.Equal(t, ReasonTLSCert, reason)
			require.Equal(t, "maps.google.com", host)
			require.Equal(t, class, gotClass)
		})
	}

	t.Run("wrapped", func(t *testing.T) {
		t.Parallel()

		_, reason, _, _, ok := classifyFetchError(fmt.Errorf("fetch: %w", verifyErr(scrapemate.TLSClassExpired)))

		require.True(t, ok)
		require.Equal(t, ReasonTLSCert, reason)
	})

	t.Run("chromium navigation error", func(t *testing.T) {
		t.Parallel()

		_, reason, host, class, ok := classifyFetchError(
			errors.New("page.goto: net::ERR_CERT_AUTHORITY_INVALID at https://www.google.com/maps/search/x"))

		require.True(t, ok)
		require.Equal(t, ReasonTLSCert, reason)
		require.Equal(t, "www.google.com", host)
		require.Equal(t, scrapemate.TLSClassUnknownAuthority, class)
	})

	notVerification := []error{
		nil,
		io.ErrUnexpectedEOF,
		errors.New("read tcp 10.0.0.1:5000->10.0.0.2:443: connection reset by peer"),
		errors.New("context deadline exceeded"),
		errors.New("407 Proxy Authentication Required"),
		errors.New("proxyconnect tcp: dial tcp 127.0.0.1:1: connect: connection refused"),
		errors.New("net::ERR_SSL_PROTOCOL_ERROR at https://www.google.com/"),
		errors.New("failed to handshake: remote error: tls: handshake failure"),
	}

	for i, err := range notVerification {
		t.Run(fmt.Sprintf("not a verification failure %d", i), func(t *testing.T) {
			t.Parallel()

			_, reason, _, _, ok := classifyFetchError(err)

			require.False(t, ok, "%v", err)
			require.Empty(t, reason)
		})
	}
}
