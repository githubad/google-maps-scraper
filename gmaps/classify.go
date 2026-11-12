package gmaps

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"

	"github.com/gosom/scrapemate"
)

// BlockReason is why a response was classified as a block (docs/contracts.md
// §4.2). The rules are checked in the order of the constants below; the first
// match wins.
type BlockReason string

// Block reasons, exactly as written in the search report.
const (
	// ReasonTLSCert is checked first: a certificate that fails verification has no
	// HTTP status and no body, so no other rule can apply (contract 1.3.0).
	ReasonTLSCert        BlockReason = "tls_cert"
	ReasonHTTP429        BlockReason = "http_429"
	ReasonHTTP503        BlockReason = "http_503"
	ReasonSorryRedirect  BlockReason = "sorry_redirect"
	ReasonCaptchaPage    BlockReason = "captcha_page"
	ReasonUnusualTraffic BlockReason = "unusual_traffic"
	ReasonNoXSSIPrefix   BlockReason = "no_xssi_prefix"
)

// PageOutcome is what the engine concluded about one page response.
type PageOutcome int

// Page outcomes. PageParseError is set by the caller after parsing; the
// classifiers never return it.
const (
	// PageOK: the response carries no block signal and no HTTP error.
	PageOK PageOutcome = iota
	// PageBlocked: one of the block reasons matched.
	PageBlocked
	// PageParseError: a 2xx page with a valid prefix whose structure could not be parsed.
	PageParseError
	// PageHTTPError: a non-2xx status that is not a block (403, 404, 500, …).
	PageHTTPError
	// PageNetworkError: no HTTP status at all (timeout, reset, TLS error).
	PageNetworkError
)

const (
	xssiPrefix        = ")]}'"
	sorryPathPrefix   = "/sorry/"
	needleRecaptchaG  = "g-recaptcha"
	needleRecaptchaAP = "recaptcha/api"
	needleUnusual     = "unusual traffic"
)

// classifyFetchError recognises a fetch that failed because the server's
// certificate did not verify (list mode: the fetcher's own typed error; browser
// mode: a Chromium net::ERR_CERT_* navigation error). It returns the page
// outcome PageBlocked with ReasonTLSCert, the host the certificate was checked
// for (may be empty) and the failure class, and ok false for every other error:
// resets, timeouts, protocol alerts and proxy failures stay network errors.
func classifyFetchError(err error) (outcome PageOutcome, reason BlockReason, host, class string, ok bool) {
	host, class, ok = scrapemate.TLSVerifyClass(err)
	if !ok {
		return PageOK, "", "", "", false
	}

	return PageBlocked, ReasonTLSCert, host, class, true
}

// ClassifyListResponse classifies one list-mode page response following
// contract §4.2: http_429, http_503, sorry_redirect, captcha_page,
// unusual_traffic, no_xssi_prefix. finalURL is the URL the response came from
// as far as the fetcher knows (it may be the request URL). The text rules do
// not look at a body that starts with the XSSI prefix: that body is data, and a
// business name or website inside it is not a block. Any other non-2xx status
// is a page error. Status 0 means the request got no HTTP response.
func ClassifyListResponse(status int, location, finalURL string, body []byte) (PageOutcome, BlockReason) {
	switch {
	case status == 0:
		return PageNetworkError, ""
	case status == http.StatusTooManyRequests:
		return PageBlocked, ReasonHTTP429
	case status == http.StatusServiceUnavailable:
		return PageBlocked, ReasonHTTP503
	case locationIsSorry(status, location) || isSorryURL(finalURL):
		return PageBlocked, ReasonSorryRedirect
	}

	isData := hasXSSIPrefix(body)

	if !isData {
		if containsFold(body, needleRecaptchaG) || containsFold(body, needleRecaptchaAP) {
			return PageBlocked, ReasonCaptchaPage
		}

		if containsFold(body, needleUnusual) {
			return PageBlocked, ReasonUnusualTraffic
		}
	}

	switch {
	case status < http.StatusOK || status >= http.StatusMultipleChoices:
		return PageHTTPError, ""
	case !isData:
		return PageBlocked, ReasonNoXSSIPrefix
	default:
		return PageOK, ""
	}
}

// ClassifyBrowserPage classifies the browser-mode search page: the navigation
// status (429, 503), the page URL (/sorry/ path) and the page content
// (captcha, unusual traffic). Other statuses are not judged here.
func ClassifyBrowserPage(status int, pageURL, content string) (PageOutcome, BlockReason) {
	switch {
	case status == http.StatusTooManyRequests:
		return PageBlocked, ReasonHTTP429
	case status == http.StatusServiceUnavailable:
		return PageBlocked, ReasonHTTP503
	case isSorryURL(pageURL):
		return PageBlocked, ReasonSorryRedirect
	}

	lower := strings.ToLower(content)

	switch {
	case strings.Contains(lower, needleRecaptchaG) || strings.Contains(lower, needleRecaptchaAP):
		return PageBlocked, ReasonCaptchaPage
	case strings.Contains(lower, needleUnusual):
		return PageBlocked, ReasonUnusualTraffic
	default:
		return PageOK, ""
	}
}

// isSorryURL reports whether the URL path starts with /sorry/. The query
// string and the rest of the URL are ignored, so a search for the word "sorry"
// (/maps/search/sorry/…) is not a redirect to Google's block page.
func isSorryURL(raw string) bool {
	if raw == "" {
		return false
	}

	if u, err := url.Parse(raw); err == nil {
		return strings.HasPrefix(u.Path, sorryPathPrefix)
	}

	// unparsable: look at the text after the host
	rest := raw
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+len("://"):]
	}

	i := strings.IndexByte(rest, '/')
	if i < 0 {
		return false
	}

	rest = rest[i:]
	if j := strings.IndexAny(rest, "?#"); j >= 0 {
		rest = rest[:j]
	}

	return strings.HasPrefix(rest, sorryPathPrefix)
}

// locationIsSorry reports whether a 3xx response points to Google's block page.
func locationIsSorry(status int, location string) bool {
	return status >= http.StatusMultipleChoices && status < http.StatusBadRequest &&
		strings.Contains(location, sorryPathPrefix)
}

// hasXSSIPrefix reports whether the body starts with )]}' after leading whitespace.
func hasXSSIPrefix(body []byte) bool {
	return bytes.HasPrefix(bytes.TrimLeft(body, " \t\r\n"), []byte(xssiPrefix))
}

// containsFold reports whether body contains the lower-case ASCII needle,
// ignoring ASCII case, without copying the body.
func containsFold(body []byte, needle string) bool {
	n := len(needle)
	if n == 0 {
		return true
	}

	first := needle[0]

	for i := 0; i+n <= len(body); i++ {
		if lowerASCII(body[i]) != first {
			continue
		}

		match := true

		for k := 1; k < n; k++ {
			if lowerASCII(body[i+k]) != needle[k] {
				match = false

				break
			}
		}

		if match {
			return true
		}
	}

	return false
}

func lowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}

	return b
}
