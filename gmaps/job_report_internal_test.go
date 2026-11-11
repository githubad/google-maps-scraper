package gmaps

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/gosom/scrapemate"
	"github.com/stretchr/testify/require"

	"github.com/gosom/google-maps-scraper/deduper"
)

func feedLinks(n int) []string {
	links := make([]string, 0, n)

	for i := 1; i <= n; i++ {
		links = append(links, fmt.Sprintf("https://www.google.com/maps/place/biz%d/data=!4m5", i))
	}

	return links
}

func feedPage(t *testing.T, links []string, meta map[string]any) *scrapemate.Response {
	t.Helper()

	var b strings.Builder

	b.WriteString(`<html><body><div role="feed">`)

	for _, l := range links {
		fmt.Fprintf(&b, `<div jsaction="x"><a href="%s">x</a></div>`, l)
	}

	b.WriteString(`</div></body></html>`)

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(b.String()))
	require.NoError(t, err)

	return &scrapemate.Response{URL: browserSearchURL, Document: doc, Meta: meta}
}

func ranksOf(t *testing.T, next []scrapemate.IJob) []int {
	t.Helper()

	out := make([]int, 0, len(next))

	for _, j := range next {
		pj, ok := j.(*PlaceJob)
		require.True(t, ok)

		out = append(out, pj.Rank)
	}

	return out
}

func TestGmapJobProcess_AS17_LinksAndRank(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	links := feedLinks(3)
	dedup := deduper.New()
	dedup.AddIfNotExists(context.Background(), links[1]) // the second link was seen earlier in the batch

	job := newBrowserJob(t, env, WithDeduper(dedup))
	_, next, err := job.Process(context.Background(), feedPage(t, links, nil))

	require.NoError(t, err)
	require.Len(t, next, 2)
	require.Equal(t, []int{1, 3}, ranksOf(t, next), "rank is the position in the feed; the seen link leaves a gap")
	require.Empty(t, env.lines(t), "the task finishes when its place jobs have")

	env.tr.PlaceDone("t_b", true)
	env.tr.PlaceDone("t_b", true)

	line := env.lines(t)[0]
	require.Equal(t, "ok", line["status"])
	require.EqualValues(t, 3, line["links_found"])
	require.EqualValues(t, 2, line["links_new"])
	require.EqualValues(t, 2, line["places_ok"])
	require.EqualValues(t, 0, line["places_failed"])
	require.Nil(t, line["bytes_in"])
	require.Nil(t, line["max_pages"])
	require.EqualValues(t, 0, line["pages_requested"])
}

func TestGmapJobProcess_AS21_AllSeenIsOK(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	links := feedLinks(3)
	dedup := deduper.New()

	for _, l := range links {
		dedup.AddIfNotExists(context.Background(), l)
	}

	exit := &recordingExiter{}
	job := newBrowserJob(t, env, WithDeduper(dedup), WithExitMonitor(exit))
	_, next, err := job.Process(context.Background(), feedPage(t, links, nil))

	require.NoError(t, err)
	require.Empty(t, next)
	require.Equal(t, 1, exit.seedsCompleted)

	line := env.lines(t)[0]
	require.Equal(t, "ok", line["status"])
	require.EqualValues(t, 3, line["links_found"])
	require.EqualValues(t, 0, line["links_new"])
	require.EqualValues(t, 0, line["places_ok"])
	require.EqualValues(t, 0, line["places_failed"])
}

func TestGmapJobProcess_EmptyFeedIsEmpty(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	job := newBrowserJob(t, env)

	_, next, err := job.Process(context.Background(), feedPage(t, nil, nil))

	require.NoError(t, err)
	require.Empty(t, next)
	require.Equal(t, "empty", env.lines(t)[0]["status"])
	require.EqualValues(t, 0, env.lines(t)[0]["links_found"])
}

func TestGmapJobProcess_SinglePlaceRedirect(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	job := newBrowserJob(t, env)
	resp := feedPage(t, nil, nil)
	resp.URL = browserPlaceURL

	_, next, err := job.Process(context.Background(), resp)

	require.NoError(t, err)
	require.Len(t, next, 1)
	require.Equal(t, []int{1}, ranksOf(t, next))

	env.tr.PlaceDone("t_b", true)

	line := env.lines(t)[0]
	require.Equal(t, "ok", line["status"])
	require.EqualValues(t, 1, line["links_found"])
	require.EqualValues(t, 1, line["links_new"])
}

func TestGmapJobProcess_OutcomesFinishTheTask(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		o      browserOutcome
		status string
		reason string
		errMsg string
	}{
		{"blocked", browserOutcome{Kind: browserBlocked, Reason: ReasonCaptchaPage}, "blocked", "captcha_page", ""},
		{"cant find", browserOutcome{Kind: browserEmpty}, "empty", "", ""},
		{"no feed", browserOutcome{Kind: browserFailed, Err: "results list not found"}, "error", "", "results list not found"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env := newTrackerEnv(t)
			exit := &recordingExiter{}
			job := newBrowserJob(t, env, WithExitMonitor(exit))

			_, next, err := job.Process(context.Background(), feedPage(t, feedLinks(2), map[string]any{browserOutcomeKey: tc.o}))

			require.NoError(t, err)
			require.Empty(t, next, "no place jobs after a block, an empty page or a failure")
			require.Equal(t, 1, exit.seedsCompleted)

			line := env.lines(t)[0]
			require.Equal(t, tc.status, line["status"])

			if tc.reason == "" {
				require.Nil(t, line["block_reason"])
			} else {
				require.Equal(t, tc.reason, line["block_reason"])
			}

			if tc.errMsg == "" {
				require.Nil(t, line["error"])
			} else {
				require.Equal(t, tc.errMsg, line["error"])
			}
		})
	}
}

func TestGmapJobProcess_FetchErrorFinishesAsError(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	job := newBrowserJob(t, env)

	_, _, err := job.Process(context.Background(), &scrapemate.Response{Error: context.DeadlineExceeded})
	require.NoError(t, err)

	line := env.lines(t)[0]
	require.Equal(t, "error", line["status"])
	require.Equal(t, context.DeadlineExceeded.Error(), line["error"])
}

func TestGmapJobProcess_CancelledRunLeavesTheTaskForFinalize(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	job := newBrowserJob(t, env)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := job.Process(ctx, &scrapemate.Response{Error: context.Canceled})
	require.NoError(t, err)
	require.Empty(t, env.lines(t))

	env.tr.FinalizeAll(ErrInterrupted)
	require.Equal(t, "interrupted", env.lines(t)[0]["error"])
}

func TestGmapJobProcess_FinishWhenPlacesDone(t *testing.T) {
	t.Parallel()

	run := func(t *testing.T, results ...bool) map[string]any {
		t.Helper()

		env := newTrackerEnv(t)
		job := newBrowserJob(t, env)

		_, next, err := job.Process(context.Background(), feedPage(t, feedLinks(len(results)), nil))
		require.NoError(t, err)
		require.Len(t, next, len(results))

		for i, ok := range results {
			require.Empty(t, env.lines(t), "not finished before place %d", i)
			env.tr.PlaceDone("t_b", ok)
		}

		return env.lines(t)[0]
	}

	line := run(t, true, true, false)
	require.Equal(t, "ok", line["status"], "ok if at least one place was parsed")
	require.EqualValues(t, 2, line["places_ok"])
	require.EqualValues(t, 1, line["places_failed"])

	line = run(t, false, false)
	require.Equal(t, "parse_error", line["status"])
	require.NotNil(t, line["error"])
}

func TestBrowserCutOffStatuses(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		okPlaces int
		status   string
	}{"one place parsed": {1, "partial"}, "none parsed": {0, "error"}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env := newTrackerEnv(t)
			job := newBrowserJob(t, env)

			_, _, err := job.Process(context.Background(), feedPage(t, feedLinks(3), nil))
			require.NoError(t, err)

			for range tc.okPlaces {
				env.tr.PlaceDone("t_b", true)
			}

			env.tr.FinalizeAll(ErrAborted)

			line := env.lines(t)[0]
			require.Equal(t, tc.status, line["status"])
			require.Equal(t, "aborted: max-blocked", line["error"])
		})
	}
}

func TestBrowserSearchPageInFlightCutOff(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	env.tr.Register("t_b", ModeBrowser, nil)
	env.tr.FetchStarted("t_b", 0)
	env.tr.FinalizeAll(ErrInterrupted)

	line := env.lines(t)[0]
	require.Equal(t, "error", line["status"])
	require.Equal(t, "interrupted", line["error"])
}

// Without a tracker Process is upstream's: Meta is ignored and place jobs follow.
func TestGmapJobProcess_WithoutTrackerIsStock(t *testing.T) {
	t.Parallel()

	job := NewGmapJob("t_b", "en", "roofer", 1, false, "30.2,-97.7", 14)

	_, next, err := job.Process(context.Background(), feedPage(t, feedLinks(2), map[string]any{
		browserOutcomeKey: browserOutcome{Kind: browserBlocked, Reason: ReasonHTTP429},
	}))

	require.NoError(t, err)
	require.Len(t, next, 2)

	for _, j := range next {
		pj, ok := j.(*PlaceJob)
		require.True(t, ok)
		require.Zero(t, pj.Rank, "no rank without a tracker")
		require.Nil(t, pj.Tracker)
	}
}

func TestBrowserBlockedCountsTowardMaxBlocked(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)

	var fired atomic.Int32

	env.tr.SetBlockedThreshold(1, func() { fired.Add(1) })

	job := newBrowserJob(t, env)
	_, _, err := job.Process(context.Background(), feedPage(t, nil, map[string]any{
		browserOutcomeKey: browserOutcome{Kind: browserBlocked, Reason: ReasonHTTP429},
	}))

	require.NoError(t, err)
	require.EqualValues(t, 1, fired.Load())
}

func TestGmapJobPlaceJobsKeepWriterManagedCompletion(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	job := newBrowserJob(t, env, WithWriterManagedCompletion())

	_, next, err := job.Process(context.Background(), feedPage(t, feedLinks(1), nil))
	require.NoError(t, err)
	require.Len(t, next, 1)

	pj, ok := next[0].(*PlaceJob)
	require.True(t, ok)
	require.True(t, pj.WriterManagedCompletion)
}
