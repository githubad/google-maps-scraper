package filerunner

import (
	"testing"

	"github.com/gosom/scrapemate/scrapemateapp"
	"github.com/stretchr/testify/require"
)

func TestJSONLFetcherKind(t *testing.T) {
	t.Parallel()

	require.Equal(t, "stealth", fetcherKind("list", false))
	require.Equal(t, "stealth", fetcherKind("list", true), "-debug only matters for the browser")
	require.Equal(t, "stealth", fetcherKind("", false), "no tasks defaults to list")
	require.Equal(t, "js", fetcherKind("browser", false))
	require.Equal(t, "js-headfull", fetcherKind("browser", true))
}

func TestJSONLFetcherOptions(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{fetcherStealth, fetcherJS, fetcherJSHeadfull} {
		require.Len(t, jsonlFetcherOptions(kind), 1, kind)
	}
}

// FR-015 / AS-19: certificate verification is switched on for jsonl runs (list
// and browser fetchers alike) and left at upstream's default otherwise.
func TestTLSVerificationOptionIsOnlyForJSONLRuns(t *testing.T) {
	t.Parallel()

	apply := func(opts []func(*scrapemateapp.Config) error) *scrapemateapp.Config {
		cfg := &scrapemateapp.Config{}

		for _, opt := range opts {
			require.NoError(t, opt(cfg))
		}

		return cfg
	}

	require.True(t, apply(tlsOptions(true)).VerifyTLS, "jsonl runs verify certificates")
	require.False(t, apply(tlsOptions(false)).VerifyTLS, "stock lines mode keeps upstream's behaviour")
	require.Empty(t, tlsOptions(false))
}
