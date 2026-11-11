package gmaps

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/gosom/scrapemate"
	"github.com/stretchr/testify/require"
)

const feedHTML = `<html><body><div role="feed">
<div jsaction="x"><a href="https://www.google.com/maps/place/a">A</a></div>
<div jsaction="x"><a href="https://www.google.com/maps/place/b">B</a></div>
</div></body></html>`

func feedResponse(t *testing.T) *scrapemate.Response {
	t.Helper()

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(feedHTML))
	require.NoError(t, err)

	return &scrapemate.Response{URL: "https://www.google.com/maps/search/x", Document: doc}
}

func TestGmapJobProcessSleepsAfterSearchPage(t *testing.T) {
	t.Parallel()

	const delay = 150 * time.Millisecond

	exit := &recordingExiter{}
	job := NewGmapJob("t_b", "en", "roofer", 1, false, "30.2,-97.7", 14,
		WithExitMonitor(exit), WithGmapJobDelay(delay, delay))

	start := time.Now()
	_, next, err := job.Process(context.Background(), feedResponse(t))

	require.NoError(t, err)
	require.Len(t, next, 2)
	require.GreaterOrEqual(t, time.Since(start), delay)
	require.Equal(t, 1, exit.seedsCompleted)
}

func TestGmapJobNoDelayByDefault(t *testing.T) {
	t.Parallel()

	job := NewGmapJob("t_b", "en", "roofer", 1, false, "30.2,-97.7", 14)

	require.Zero(t, job.DelayMin)
	require.Zero(t, job.DelayMax)

	_, _, err := job.Process(context.Background(), feedResponse(t))
	require.NoError(t, err)
}
