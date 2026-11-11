package filerunner

import (
	"testing"

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
