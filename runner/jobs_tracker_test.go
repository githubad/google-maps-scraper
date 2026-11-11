package runner_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gosom/google-maps-scraper/gmaps"
	"github.com/gosom/google-maps-scraper/runner"
)

func TestCreateJSONLSeedJobsWithTracker(t *testing.T) {
	t.Parallel()

	tracker := gmaps.NewTaskTracker(nil, "v", nil)
	tasks := []runner.SearchTask{
		{ID: "t_1", Query: "roofer", Mode: runner.ModeList, Lat: 30.2, Lng: -97.7, Zoom: 14},
		{ID: "t_2", Query: "plumber", Mode: runner.ModeList, Lat: 30.2, Lng: -97.7, Zoom: 14},
	}

	jobs, err := runner.CreateJSONLSeedJobs(tasks, jsonlConfig(), nil, nil, runner.WithTracker(tracker))
	require.NoError(t, err)
	require.Len(t, jobs, 2)

	for _, j := range jobs {
		sj, ok := j.(*gmaps.SearchJob)
		require.True(t, ok)
		require.Same(t, tracker, sj.Tracker)
		require.Equal(t, 2, sj.GetMaxRetries(), "network failures are retried twice")
	}

	require.Equal(t, 2, tracker.Counts().Tasks, "every list task is registered before it runs")
}

func TestCreateJSONLSeedJobsWithoutTrackerIsStock(t *testing.T) {
	t.Parallel()

	tasks := []runner.SearchTask{{ID: "t_1", Query: "roofer", Mode: runner.ModeList, Lat: 30.2, Lng: -97.7, Zoom: 14}}

	jobs, err := runner.CreateJSONLSeedJobs(tasks, jsonlConfig(), nil, nil)
	require.NoError(t, err)

	sj, ok := jobs[0].(*gmaps.SearchJob)
	require.True(t, ok)
	require.Nil(t, sj.Tracker)
	require.Nil(t, sj.CheckResponse)
	require.Equal(t, 3, sj.GetMaxRetries())
}

func TestCreateJSONLSeedJobsWithAbortGate(t *testing.T) {
	t.Parallel()

	tracker := gmaps.NewTaskTracker(nil, "v", nil)
	gate := gmaps.NewAbortController(0)
	tasks := []runner.SearchTask{{ID: "t_1", Query: "roofer", Mode: runner.ModeList, Lat: 30.2, Lng: -97.7, Zoom: 14}}

	jobs, err := runner.CreateJSONLSeedJobs(tasks, jsonlConfig(), nil, nil, runner.WithTracker(tracker), runner.WithAbortGate(gate))
	require.NoError(t, err)

	sj, ok := jobs[0].(*gmaps.SearchJob)
	require.True(t, ok)
	require.Same(t, gate, sj.Gate)
}

func TestCreateJSONLSeedJobsBrowserTasksCarryTracker(t *testing.T) {
	t.Parallel()

	tracker := gmaps.NewTaskTracker(nil, "v", nil)
	gate := gmaps.NewAbortController(0)
	tasks := []runner.SearchTask{
		{ID: "t_b", Query: "roofer", Mode: runner.ModeBrowser, Lat: 30.2, Lng: -97.7, Zoom: 14},
	}

	cfg := jsonlConfig()
	cfg.DelayMin, cfg.DelayMax = 0, 0

	jobs, err := runner.CreateJSONLSeedJobs(tasks, cfg, nil, nil, runner.WithTracker(tracker), runner.WithAbortGate(gate))
	require.NoError(t, err)

	gj, ok := jobs[0].(*gmaps.GmapJob)
	require.True(t, ok)
	require.Same(t, tracker, gj.Tracker)
	require.Same(t, gate, gj.Gate)
	require.Equal(t, 1, tracker.Counts().Tasks, "a browser task is registered before it runs")
}

func TestCreateJSONLSeedJobsBrowserWithoutTrackerIsStock(t *testing.T) {
	t.Parallel()

	tasks := []runner.SearchTask{{ID: "t_b", Query: "roofer", Mode: runner.ModeBrowser, Lat: 30.2, Lng: -97.7, Zoom: 14}}

	jobs, err := runner.CreateJSONLSeedJobs(tasks, jsonlConfig(), nil, nil)
	require.NoError(t, err)

	gj, ok := jobs[0].(*gmaps.GmapJob)
	require.True(t, ok)
	require.Nil(t, gj.Tracker)
	require.Nil(t, gj.Gate)
}
