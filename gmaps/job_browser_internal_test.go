package gmaps

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	browserSorryURL  = "https://www.google.com/sorry/index?continue=https://www.google.com/maps/search/roofer"
	browserSearchURL = "https://www.google.com/maps/search/roofer/@30.2,-97.7,14z"
	browserPlaceURL  = "https://www.google.com/maps/place/Acme+Roofing/@30.2,-97.7,17z/data=!4m5"
)

func newBrowserJob(t *testing.T, env trackerEnv, opts ...GmapJobOptions) *GmapJob {
	t.Helper()

	env.tr.Register("t_b", ModeBrowser, nil)

	all := append([]GmapJobOptions{WithGmapJobTracker(env.tr)}, opts...)

	return NewGmapJob("t_b", "en", "roofer", 1, false, "30.2,-97.7", 14, all...)
}

// shortCtx bounds the single-place wait of BrowserActions (5 s in production).
func shortCtx(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	t.Cleanup(cancel)

	return ctx
}

func outcomeOf(t *testing.T, resp map[string]any) browserOutcome {
	t.Helper()

	o, ok := resp[browserOutcomeKey].(browserOutcome)
	require.True(t, ok, "BrowserActions must leave an outcome in resp.Meta")

	return o
}

func TestBrowser_AS11_SorryURL(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	job := newBrowserJob(t, env)
	page := &fakePage{navStatus: 200, navURL: browserSorryURL}

	resp := job.BrowserActions(shortCtx(t), page)

	o := outcomeOf(t, resp.Meta)
	require.Equal(t, browserBlocked, o.Kind)
	require.Equal(t, ReasonSorryRedirect, o.Reason)
	require.Zero(t, page.feedWaits, "a block page is not waited on for a feed")
	require.NotEmpty(t, env.tr.startedAt("t_b"), "the start time is recorded when the navigation begins")
}

func TestBrowser_AS11_CantFind(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	job := newBrowserJob(t, env)
	page := &fakePage{navStatus: 200, navURL: browserSearchURL, content: "<html>Google Maps CAN'T FIND that place.</html>"}

	resp := job.BrowserActions(shortCtx(t), page)

	require.Equal(t, browserEmpty, outcomeOf(t, resp.Meta).Kind)
}

func TestBrowser_FeedMissingUnknown(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	job := newBrowserJob(t, env)
	page := &fakePage{navStatus: 200, navURL: browserSearchURL, content: "<html>something else</html>"}

	o := outcomeOf(t, job.BrowserActions(shortCtx(t), page).Meta)

	require.Equal(t, browserFailed, o.Kind)
	require.Equal(t, "results list not found", o.Err)
}

func TestBrowser_ContentRules(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		reason  BlockReason
	}{
		{"captcha", `<div class="g-recaptcha"></div>`, ReasonCaptchaPage},
		{"unusual traffic", "<p>Unusual Traffic from your network</p>", ReasonUnusualTraffic},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env := newTrackerEnv(t)
			job := newBrowserJob(t, env)
			page := &fakePage{navStatus: 200, navURL: browserSearchURL, content: tc.content}

			o := outcomeOf(t, job.BrowserActions(shortCtx(t), page).Meta)

			require.Equal(t, browserBlocked, o.Kind)
			require.Equal(t, tc.reason, o.Reason)
		})
	}
}

func TestBrowser_AS25_NavStatus(t *testing.T) {
	t.Parallel()

	for status, reason := range map[int]BlockReason{429: ReasonHTTP429, 503: ReasonHTTP503} {
		env := newTrackerEnv(t)
		job := newBrowserJob(t, env)
		page := &fakePage{navStatus: status, navURL: browserSearchURL}

		o := outcomeOf(t, job.BrowserActions(shortCtx(t), page).Meta)

		require.Equal(t, browserBlocked, o.Kind, status)
		require.Equal(t, reason, o.Reason, status)
		require.Zero(t, page.feedWaits, "no feed wait after a blocked navigation")
	}
}

func TestBrowser_AS18_SearchTextSorryIsNotBlocked(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	job := newBrowserJob(t, env)
	url := "https://www.google.com/maps/search/sorry/@30.2,-97.7,14z"
	page := &fakePage{navStatus: 200, navURL: url, feedPresent: true, content: "<div role=feed></div>"}

	resp := job.BrowserActions(shortCtx(t), page)

	require.NoError(t, resp.Error)
	require.NotEmpty(t, resp.Body, "the page content is returned for Process")

	if o, ok := resp.Meta[browserOutcomeKey].(browserOutcome); ok {
		require.Equal(t, browserOK, o.Kind)
	}
}

func TestBrowser_SinglePlaceRedirect(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	job := newBrowserJob(t, env)
	page := &fakePage{navStatus: 200, navURL: browserSearchURL, redirectedTo: browserPlaceURL, content: "<html>place</html>"}

	resp := job.BrowserActions(context.Background(), page)

	require.NoError(t, resp.Error)
	require.Equal(t, browserPlaceURL, resp.URL)
	require.NotEmpty(t, resp.Body)
}

func TestBrowser_NavigationErrorKeepsError(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	job := newBrowserJob(t, env)
	page := &fakePage{navErr: errNoFeed}

	require.ErrorIs(t, job.BrowserActions(shortCtx(t), page).Error, errNoFeed)
}

// Without a tracker the stock flow is unchanged: a /sorry/ page is still waited
// on for a feed and nothing is left in Meta.
func TestBrowser_WithoutTrackerIsStock(t *testing.T) {
	t.Parallel()

	job := NewGmapJob("t_b", "en", "roofer", 1, false, "30.2,-97.7", 14)
	page := &fakePage{navStatus: 200, navURL: browserSorryURL}

	resp := job.BrowserActions(shortCtx(t), page)

	require.Positive(t, page.feedWaits)
	require.Nil(t, resp.Meta)
}
