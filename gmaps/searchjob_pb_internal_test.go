package gmaps

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// upstreamPB is a literal copy of upstream's request template (v1.18.1) with
// its fixed !1d and the zoom in !4f. The stock builder must keep producing it.
func upstreamPB(lon, lat float64, w, h int, zoom float64) string {
	return fmt.Sprintf("!4m12!1m3!1d3826.902183192154!2d%.4f!3d%.4f!2m3!1f0!2f0!3f0!3m2!1i%d!2i%d!4f%.1f!7i20!8i0"+
		"!10b1!12m22!1m3!18b1!30b1!34e1!2m3!5m1!6e2!20e3!4b0!10b1!12b1!13b1!16b1!17m1!3e1!20m3!5e2!6b1!14b1!46m1!1b0"+
		"!96b1!19m4!2m3!1i360!2i120!4i8",
		lon, lat, w, h, zoom,
	)
}

func pbTokens(pb string) []string {
	return strings.Split(strings.TrimPrefix(pb, "!"), "!")
}

// withoutTokens removes the tokens that start with any of the given prefixes.
func withoutTokens(tokens []string, prefixes ...string) []string {
	return slices.DeleteFunc(slices.Clone(tokens), func(tok string) bool {
		for _, p := range prefixes {
			if strings.HasPrefix(tok, p) {
				return true
			}
		}

		return false
	})
}

var austin = MapLocation{Lat: 30.2672, Lon: -97.7431}

func TestBuildGoogleMapsParams_SpanAndViewport(t *testing.T) {
	// AS-8
	params := &MapSearchParams{
		TaskMode: true, SpanM: 5000, ViewportW: 800, ViewportH: 600, Offset: 40,
		Location: austin, Query: "roofer", Hl: "en",
	}

	pb := buildParams(params)["pb"]
	tokens := pbTokens(pb)

	for _, want := range []string{"1d21773.27", "4f13.1", "1i800", "2i600", "7i20", "8i40"} {
		require.Contains(t, tokens, want)
	}

	upstream := pbTokens(upstreamPB(austin.Lon, austin.Lat, 800, 600, 14))
	require.Equal(t,
		withoutTokens(upstream, "1d", "4f", "1i", "2i", "8i"),
		withoutTokens(tokens, "1d", "4f", "1i", "2i", "8i"),
		"every other token equals upstream's template")
	require.Contains(t, tokens, "2d-97.7431")
	require.Contains(t, tokens, "3d30.2672")
}

func TestBuildGoogleMapsParams_ZoomDerivedSpan(t *testing.T) {
	// AS-9
	span := derivedSpanM(30.2672, 13, 800)
	require.InDelta(t, 13203.5, span, 1)
	require.InDelta(t, 57496.63, altitudeFromSpan(span), 5)

	chrome := derivedSpanM(30.2672, 13, 889)
	require.InDelta(t, 14672.4, chrome, 1)
	require.InDelta(t, 14656, chrome, 14656*0.002+30, "within 0.2% of the first HAR request")

	pb := buildParams(&MapSearchParams{TaskMode: true, Location: MapLocation{Lat: 30.2672, Lon: -97.7431, ZoomLvl: 13}, ViewportW: 600, ViewportH: 800})["pb"]
	require.Contains(t, pbTokens(pb), "4f13.1")
}

func TestBuildGoogleMapsParams_SpanClamp(t *testing.T) {
	require.InDelta(t, 100, derivedSpanM(80, 21, 800), 1e-9)
	require.InDelta(t, 200000, derivedSpanM(0, 3, 8192), 1e-9)
	require.InDelta(t, 435.47, altitudeFromSpan(100), 0.01)

	pb := buildParams(&MapSearchParams{TaskMode: true, SpanM: 100, Location: austin})["pb"]
	require.Contains(t, pbTokens(pb), "1d435.47", "two decimals")

	frac := buildParams(&MapSearchParams{TaskMode: true, Location: MapLocation{Lat: 10, Lon: 10, ZoomLvl: 13.5}})["pb"]
	require.NotEmpty(t, frac)

	params := buildParams(&MapSearchParams{TaskMode: true, SpanM: 500, Location: austin, Query: "q", Hl: "de"})
	require.Equal(t, "map", params["tbm"])
	require.Equal(t, "0", params["authuser"])
	require.Equal(t, "de", params["hl"])
	require.Equal(t, "q", params["q"])
	require.Contains(t, pbTokens(params["pb"]), "1i600", "zero viewport falls back to 600x800")
	require.Contains(t, pbTokens(params["pb"]), "2i800")
	require.False(t, math.IsNaN(altitudeFromSpan(1)))
}

func TestBuildGoogleMapsParams_LegacyIdentical(t *testing.T) {
	// AS-10 (request half): stock requests are upstream's string byte for byte
	cases := []struct {
		lat, lon, zoom float64
	}{
		{30.2672, -97.7431, 15},
		{-33.8688, 151.2093, 13},
		{0, 0, 3},
		{51.5072, -0.1276, 21},
	}

	for _, c := range cases {
		want := upstreamPB(c.lon, c.lat, 600, 800, c.zoom)

		got := buildGoogleMapsParams(&MapSearchParams{
			Location: MapLocation{Lat: c.lat, Lon: c.lon, ZoomLvl: c.zoom}, ViewportW: 1920, ViewportH: 450,
		})["pb"]
		require.Equal(t, want, got)
		require.Contains(t, got, "!1d3826.902183192154")

		// the web-runner shape (zero-value task fields) and the dispatcher agree
		job := NewSearchJob(&MapSearchParams{Location: MapLocation{Lat: c.lat, Lon: c.lon, ZoomLvl: c.zoom}},
			WithSearchJobWriterManagedCompletion())
		require.Equal(t, want, job.URLParams["pb"])
	}
}
