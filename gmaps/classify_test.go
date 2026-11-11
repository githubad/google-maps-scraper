package gmaps_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gosom/google-maps-scraper/gmaps"
)

const (
	xssi         = ")]}'\n"
	sorryURL     = "https://www.google.com/sorry/index?continue=https://maps.google.com/search"
	searchURL    = "https://maps.google.com/search?tbm=map&q=roofer"
	htmlCaptcha  = "<html><div class=\"g-recaptcha\"></div></html>"
	htmlUnusual  = "<html>Our systems have detected UNUSUAL traffic from your computer network.</html>"
	htmlBoth     = "<html>g-recaptcha ... unusual traffic</html>"
	jsonNoPrefix = `{"a":1}`
)

func TestClassifyListResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   int
		location string
		finalURL string
		body     string
		outcome  gmaps.PageOutcome
		reason   gmaps.BlockReason
	}{
		{name: "429", status: 429, outcome: gmaps.PageBlocked, reason: gmaps.ReasonHTTP429},
		{name: "503", status: 503, outcome: gmaps.PageBlocked, reason: gmaps.ReasonHTTP503},
		{name: "429 with unusual traffic body keeps http_429 (AS-1)", status: 429, body: htmlUnusual, outcome: gmaps.PageBlocked, reason: gmaps.ReasonHTTP429},
		{name: "503 with captcha body keeps http_503", status: 503, body: htmlCaptcha, outcome: gmaps.PageBlocked, reason: gmaps.ReasonHTTP503},
		{name: "302 to sorry", status: 302, location: sorryURL, outcome: gmaps.PageBlocked, reason: gmaps.ReasonSorryRedirect},
		{name: "200 with sorry final url", status: 200, finalURL: sorryURL, body: xssi + "[]", outcome: gmaps.PageBlocked, reason: gmaps.ReasonSorryRedirect},
		{name: "200 html g-recaptcha (AS-2)", status: 200, body: htmlCaptcha, outcome: gmaps.PageBlocked, reason: gmaps.ReasonCaptchaPage},
		{name: "200 html recaptcha/api mixed case", status: 200, body: "<script src=\"https://www.google.com/ReCaptcha/API.js\"></script>", outcome: gmaps.PageBlocked, reason: gmaps.ReasonCaptchaPage},
		{name: "200 html unusual traffic mixed case", status: 200, body: htmlUnusual, outcome: gmaps.PageBlocked, reason: gmaps.ReasonUnusualTraffic},
		{name: "200 json without prefix (AS-3)", status: 200, body: jsonNoPrefix, outcome: gmaps.PageBlocked, reason: gmaps.ReasonNoXSSIPrefix},
		{name: "200 empty body", status: 200, body: "", outcome: gmaps.PageBlocked, reason: gmaps.ReasonNoXSSIPrefix},
		{name: "200 valid prefix", status: 200, finalURL: searchURL, body: xssi + "[[1]]", outcome: gmaps.PageOK},
		{name: "200 leading whitespace before prefix", status: 200, body: "  \n" + xssi + "[[1]]", outcome: gmaps.PageOK},
		{name: "search url with sorry in the text is not a redirect", status: 200, finalURL: "https://maps.google.com/maps/search/sorry/", body: xssi + "[]", outcome: gmaps.PageOK},
		{name: "403 is a page error", status: 403, body: "forbidden", outcome: gmaps.PageHTTPError},
		{name: "404 is a page error", status: 404, outcome: gmaps.PageHTTPError},
		{name: "500 is a page error", status: 500, outcome: gmaps.PageHTTPError},
		{name: "301 to a normal url is a page error", status: 301, location: "https://maps.google.com/other", outcome: gmaps.PageHTTPError},
		{name: "status 0 is a network error", status: 0, outcome: gmaps.PageNetworkError},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			outcome, reason := gmaps.ClassifyListResponse(tc.status, tc.location, tc.finalURL, []byte(tc.body))

			require.Equal(t, tc.outcome, outcome)
			require.Equal(t, tc.reason, reason)
		})
	}
}

func TestClassifyListResponse_Precedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   int
		location string
		body     string
		reason   gmaps.BlockReason
	}{
		{"http_429 before http_503 signals", 429, "", htmlBoth, gmaps.ReasonHTTP429},
		{"http_503 before sorry", 503, sorryURL, htmlBoth, gmaps.ReasonHTTP503},
		{"sorry before captcha", 302, sorryURL, htmlBoth, gmaps.ReasonSorryRedirect},
		{"captcha before unusual traffic", 200, "", htmlBoth, gmaps.ReasonCaptchaPage},
		{"unusual traffic before missing prefix", 200, "", htmlUnusual, gmaps.ReasonUnusualTraffic},
		{"missing prefix last", 200, "", jsonNoPrefix, gmaps.ReasonNoXSSIPrefix},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			outcome, reason := gmaps.ClassifyListResponse(tc.status, tc.location, "", []byte(tc.body))

			require.Equal(t, gmaps.PageBlocked, outcome)
			require.Equal(t, tc.reason, reason)
		})
	}
}

func TestClassifyListResponse_JSONBodyNotScanned(t *testing.T) {
	t.Parallel()

	data := xssi + `[["Unusual Traffic Control Services","https://x.example/recaptcha/api","g-recaptcha"]]`

	outcome, reason := gmaps.ClassifyListResponse(200, "", "", []byte(data))
	require.Equal(t, gmaps.PageOK, outcome)
	require.Empty(t, reason)

	// the same text without the prefix is a block page
	outcome, reason = gmaps.ClassifyListResponse(200, "", "", []byte("<p>Unusual Traffic Control Services</p>"))
	require.Equal(t, gmaps.PageBlocked, outcome)
	require.Equal(t, gmaps.ReasonUnusualTraffic, reason)

	outcome, reason = gmaps.ClassifyListResponse(200, "", "", []byte("<a href=\"https://x.example/recaptcha/api\">x</a>"))
	require.Equal(t, gmaps.PageBlocked, outcome)
	require.Equal(t, gmaps.ReasonCaptchaPage, reason)
}

func TestClassifyBrowserPage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  int
		url     string
		content string
		outcome gmaps.PageOutcome
		reason  gmaps.BlockReason
	}{
		{name: "429 navigation", status: 429, url: searchURL, outcome: gmaps.PageBlocked, reason: gmaps.ReasonHTTP429},
		{name: "503 navigation", status: 503, url: searchURL, outcome: gmaps.PageBlocked, reason: gmaps.ReasonHTTP503},
		{name: "sorry url", status: 200, url: sorryURL, outcome: gmaps.PageBlocked, reason: gmaps.ReasonSorryRedirect},
		{name: "search text sorry is not blocked", status: 200, url: "https://www.google.com/maps/search/sorry/@30.2,-97.7,14z", content: "<div role=feed></div>", outcome: gmaps.PageOK},
		{name: "captcha content", status: 200, url: searchURL, content: htmlCaptcha, outcome: gmaps.PageBlocked, reason: gmaps.ReasonCaptchaPage},
		{name: "unusual traffic content", status: 200, url: searchURL, content: htmlUnusual, outcome: gmaps.PageBlocked, reason: gmaps.ReasonUnusualTraffic},
		{name: "normal page", status: 200, url: searchURL, content: "<div role=feed></div>", outcome: gmaps.PageOK},
		{name: "403 navigation alone is not a browser block", status: 403, url: searchURL, content: "<div role=feed></div>", outcome: gmaps.PageOK},
		{name: "status 0 with a normal page", status: 0, url: searchURL, content: "<div></div>", outcome: gmaps.PageOK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			outcome, reason := gmaps.ClassifyBrowserPage(tc.status, tc.url, tc.content)

			require.Equal(t, tc.outcome, outcome)
			require.Equal(t, tc.reason, reason)
		})
	}
}

func TestClassifyListResponse_TextRulesApplyToErrorStatuses(t *testing.T) {
	t.Parallel()

	outcome, reason := gmaps.ClassifyListResponse(403, "", "", []byte(htmlCaptcha))
	require.Equal(t, gmaps.PageBlocked, outcome)
	require.Equal(t, gmaps.ReasonCaptchaPage, reason)

	outcome, reason = gmaps.ClassifyListResponse(500, "", "", []byte(htmlUnusual))
	require.Equal(t, gmaps.PageBlocked, outcome)
	require.Equal(t, gmaps.ReasonUnusualTraffic, reason)
}
