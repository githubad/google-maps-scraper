package gmaps

import (
	"context"
	"encoding/json"
	"os"
	"sync/atomic"
	"testing"

	"github.com/gosom/scrapemate"
	"github.com/stretchr/testify/require"
)

func placeResponse(t *testing.T) *scrapemate.Response {
	t.Helper()

	raw, err := os.ReadFile("../testdata/raw.json")
	require.NoError(t, err)

	return &scrapemate.Response{Meta: map[string]any{"json": raw}}
}

func registerBrowserTask(env trackerEnv) {
	env.tr.Register("t_b", ModeBrowser, nil)
	env.tr.FetchStarted("t_b", 0)
	env.tr.BrowserLinks("t_b", 2, 2)
}

func TestPlaceJob_RankAndPage(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	registerBrowserTask(env)

	job := NewPlaceJob("t_b", "en", browserPlaceURL, false, false, WithPlaceJobTracker(env.tr), WithPlaceJobRank(3))

	res, next, err := job.Process(context.Background(), placeResponse(t))
	require.NoError(t, err)
	require.Empty(t, next)

	entry, ok := res.(*Entry)
	require.True(t, ok)
	require.Equal(t, 3, entry.Rank)
	require.Zero(t, entry.Page)
	require.Equal(t, "t_b", entry.ID)

	raw, err := json.Marshal(entry)
	require.NoError(t, err)

	var m map[string]any

	require.NoError(t, json.Unmarshal(raw, &m))
	require.EqualValues(t, 0, m["page"], "page 0 is written together with rank")
	require.EqualValues(t, 3, m["rank"])
}

func TestPlaceJob_NoRankWritesNoKeys(t *testing.T) {
	t.Parallel()

	job := NewPlaceJob("t_b", "en", browserPlaceURL, false, false)

	res, _, err := job.Process(context.Background(), placeResponse(t))
	require.NoError(t, err)

	raw, err := json.Marshal(res)
	require.NoError(t, err)

	var m map[string]any

	require.NoError(t, json.Unmarshal(raw, &m))
	require.NotContains(t, m, "page")
	require.NotContains(t, m, "rank")
}

func TestPlaceJob_PlaceDoneOnEveryPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		resp   func(t *testing.T) *scrapemate.Response
		wantOK bool
	}{
		{"fetch error", func(*testing.T) *scrapemate.Response { return &scrapemate.Response{Error: context.DeadlineExceeded} }, false},
		{"missing json", func(*testing.T) *scrapemate.Response { return &scrapemate.Response{Meta: map[string]any{}} }, false},
		{"unparsable json", func(*testing.T) *scrapemate.Response {
			return &scrapemate.Response{Meta: map[string]any{"json": []byte("not json")}}
		}, false},
		{"success", placeResponse, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env := newTrackerEnv(t)
			registerBrowserTask(env)

			exit := &recordingExiter{}
			job := NewPlaceJob("t_b", "en", browserPlaceURL, false, false, WithPlaceJobTracker(env.tr), WithPlaceJobExitMonitor(exit))

			_, _, _ = job.Process(context.Background(), tc.resp(t))

			// two expected places: one report, so the task is not finished yet
			require.Empty(t, env.lines(t))
			require.Equal(t, 1, exit.placesDone, "the exit monitor still hears about the place")

			env.tr.PlaceDone("t_b", true)

			line := env.lines(t)[0]

			if tc.wantOK {
				require.EqualValues(t, 2, line["places_ok"])
				require.EqualValues(t, 0, line["places_failed"])
			} else {
				require.EqualValues(t, 1, line["places_ok"])
				require.EqualValues(t, 1, line["places_failed"])
			}
		})
	}
}

func TestPlaceJob_GateIsToldWhenProcessed(t *testing.T) {
	t.Parallel()

	ctl := NewAbortController(0)
	ctl.begin()

	job := NewPlaceJob("t_b", "en", browserPlaceURL, false, false, WithPlaceJobGate(ctl))
	_, _, err := job.Process(context.Background(), &scrapemate.Response{Error: context.DeadlineExceeded})

	require.Error(t, err, "the fetch error is still returned")
	require.Zero(t, ctl.inFlightCount())
}

func TestGmapJobPassesGateToPlaceJobs(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	ctl := NewAbortController(0)
	job := newBrowserJob(t, env, WithGmapJobGate(ctl))

	_, next, err := job.Process(context.Background(), feedPage(t, feedLinks(2), nil))
	require.NoError(t, err)

	var gated atomic.Int32

	for _, j := range next {
		pj, ok := j.(*PlaceJob)
		require.True(t, ok)

		if pj.Gate == ctl && pj.Tracker == env.tr {
			gated.Add(1)
		}
	}

	require.EqualValues(t, 2, gated.Load(), "place jobs carry the tracker and the gate of their search job")
}
