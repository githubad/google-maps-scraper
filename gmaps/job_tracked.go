package gmaps

import (
	"context"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/gosom/scrapemate"
)

// browserOutcomeKey is the resp.Meta key under which BrowserActions leaves what
// it concluded about the search page (jsonl runs only).
const browserOutcomeKey = "gmx_outcome"

type browserKind int

const (
	browserOK browserKind = iota
	browserBlocked
	browserEmpty
	browserFailed
)

// browserOutcome is the verdict on a browser search page.
type browserOutcome struct {
	Kind   browserKind
	Reason BlockReason
	Err    string
}

// feedSelector finds the result links in the search feed.
const feedSelector = `div[role=feed] div[jsaction]>a`

// WithGmapJobTracker makes the job report to t (jsonl runs).
func WithGmapJobTracker(t *TaskTracker) GmapJobOptions {
	return func(j *GmapJob) {
		j.Tracker = t
	}
}

// WithGmapJobGate connects the job, and the place jobs it creates, to the
// early-abort gate.
func WithGmapJobGate(g *AbortController) GmapJobOptions {
	return func(j *GmapJob) {
		j.Gate = g
	}
}

// navigationOutcome classifies the navigation of the search page (HTTP status
// and URL) before any content is read.
func navigationOutcome(status int, pageURL string) (browserOutcome, bool) {
	if outcome, reason := ClassifyBrowserPage(status, pageURL, ""); outcome == PageBlocked {
		return browserOutcome{Kind: browserBlocked, Reason: reason}, true
	}

	return browserOutcome{}, false
}

// noFeedOutcome decides what a search page without a results feed is: a block
// page, Google's "can't find" page, or an unknown failure.
func noFeedOutcome(page scrapemate.BrowserPage) browserOutcome {
	content, err := page.Content()
	if err != nil {
		return browserOutcome{Kind: browserFailed, Err: err.Error()}
	}

	if outcome, reason := ClassifyBrowserPage(0, page.URL(), content); outcome == PageBlocked {
		return browserOutcome{Kind: browserBlocked, Reason: reason}
	}

	lower := strings.ReplaceAll(strings.ToLower(content), "’", "'")
	if strings.Contains(lower, "google maps can't find") {
		return browserOutcome{Kind: browserEmpty}
	}

	return browserOutcome{Kind: browserFailed, Err: "results list not found"}
}

// processTracked is Process for a browser task that reports to a TaskTracker.
func (j *GmapJob) processTracked(ctx context.Context, resp *scrapemate.Response) (any, []scrapemate.IJob, error) {
	defer j.Gate.JobFinished()

	t := j.Tracker

	if resp.Error != nil {
		if ctx.Err() == nil { // not an interruption: the task failed
			t.BrowserFailed(j.ID, resp.Error.Error())
			j.endSearch(ctx)
		}

		return nil, nil, nil
	}

	if o, ok := resp.Meta[browserOutcomeKey].(browserOutcome); ok {
		switch o.Kind {
		case browserBlocked:
			t.BrowserBlocked(j.ID, o.Reason)
		case browserEmpty:
			t.BrowserEmpty(j.ID)
		case browserFailed:
			t.BrowserFailed(j.ID, o.Err)
		case browserOK:
		}

		if o.Kind != browserOK {
			j.endSearch(ctx)

			return nil, nil, nil
		}
	}

	doc, ok := resp.Document.(*goquery.Document)
	if !ok {
		t.BrowserFailed(j.ID, "could not convert to goquery document")
		j.endSearch(ctx)

		return nil, nil, nil
	}

	var (
		next  []scrapemate.IJob
		found int
	)

	if strings.Contains(resp.URL, "/maps/place/") {
		found = 1

		next = append(next, j.placeJob(resp.URL, found))
	} else {
		doc.Find(feedSelector).Each(func(_ int, s *goquery.Selection) {
			href := s.AttrOr("href", "")
			if href == "" {
				return
			}

			found++ // the rank: the link's position in the feed, seen or not

			if j.Deduper == nil || j.Deduper.AddIfNotExists(ctx, href) {
				next = append(next, j.placeJob(href, found))
			}
		})
	}

	t.BrowserLinks(j.ID, found, len(next))

	// the worker's pause comes before the exit monitor hears about the page
	_ = sleepJitter(ctx, j.DelayMin, j.DelayMax)

	if j.ExitMonitor != nil {
		j.ExitMonitor.IncrPlacesFound(len(next))
		j.ExitMonitor.IncrSeedCompleted(1)
	}

	if j.CompletionTracker != nil {
		_ = j.CompletionTracker.SeedDiscovered(j.ID, len(next))
	}

	return nil, next, nil
}

// endSearch is the end of a browser task that has no place jobs: the worker's
// pause, then the exit monitor is told the seed is complete.
func (j *GmapJob) endSearch(ctx context.Context) {
	_ = sleepJitter(ctx, j.DelayMin, j.DelayMax)

	if j.ExitMonitor != nil {
		j.ExitMonitor.IncrSeedCompleted(1)
	}
}

// placeJob builds the job for one business link of the feed; rank is the link's
// position in the feed.
func (j *GmapJob) placeJob(href string, rank int) *PlaceJob {
	opts := []PlaceJobOptions{WithPlaceJobTracker(j.Tracker), WithPlaceJobRank(rank), WithPlaceJobGate(j.Gate)}

	if j.ExitMonitor != nil {
		opts = append(opts, WithPlaceJobExitMonitor(j.ExitMonitor))
	}

	if j.WriterManagedCompletion {
		opts = append(opts, WithPlaceJobWriterManagedCompletion())
	}

	return NewPlaceJob(j.ID, j.LangCode, href, j.ExtractEmail, j.ExtractExtraReviews, opts...)
}
