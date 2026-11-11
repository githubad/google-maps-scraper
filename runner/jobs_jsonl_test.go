package runner_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/gosom/scrapemate"
	"github.com/stretchr/testify/require"

	"github.com/gosom/google-maps-scraper/deduper"
	"github.com/gosom/google-maps-scraper/exiter"
	"github.com/gosom/google-maps-scraper/gmaps"
	"github.com/gosom/google-maps-scraper/runner"
)

func floatPtr(v float64) *float64 { return &v }

func intPtr(v int) *int { return &v }

func jsonlConfig() *runner.Config {
	return &runner.Config{LangCode: "en", InputFormat: "jsonl"}
}

func TestCreateJSONLSeedJobs_List(t *testing.T) {
	t.Parallel()

	tasks := []runner.SearchTask{
		{ID: "t_1", Query: "roofer", Mode: "list", Lat: 30.2672, Lng: -97.7431, Zoom: 14},
		{ID: "t_2", Query: "techos", Mode: "list", Lat: 4.711, Lng: -74.0721, Zoom: 13.5, Lang: "es", RadiusM: floatPtr(2500)},
		{ID: "t_3", Query: "dentist", Mode: "list", Lat: 1, Lng: 2, Zoom: 10, RadiusM: floatPtr(0)},
	}

	jobs, err := runner.CreateJSONLSeedJobs(tasks, jsonlConfig(), exiter.New(), deduper.New())
	require.NoError(t, err)
	require.Len(t, jobs, 3)

	got := make([]*gmaps.SearchJob, 0, len(jobs))

	for _, j := range jobs {
		sj, ok := j.(*gmaps.SearchJob)
		require.True(t, ok, "list tasks become SearchJobs, got %T", j)

		got = append(got, sj)
	}

	for i, task := range tasks {
		require.Equal(t, task.ID, got[i].ID)
		require.Equal(t, task.ID, got[i].TaskID)
		require.NotNil(t, got[i].ExitMonitor)
		require.Equal(t, task.Query, got[i].URLParams["q"])
	}

	require.Equal(t, "en", got[0].URLParams["hl"])
	require.Equal(t, "es", got[1].URLParams["hl"], "task lang wins over -lang")

	require.True(t, got[0].NoRadiusFilter, "radius_m absent: no filter")
	require.False(t, got[1].NoRadiusFilter, "radius_m > 0: filter")
	require.True(t, got[2].NoRadiusFilter, "radius_m 0: no filter")
}

func TestCreateJSONLSeedJobs_RadiusReachesTheJob(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../testdata/output.json")
	require.NoError(t, err)

	entries, err := gmaps.ParseSearchResults(raw)
	require.NoError(t, err)

	center := entries[0]

	body := append([]byte(")]}'\n"), raw...)

	run := func(radius *float64) int {
		task := runner.SearchTask{
			ID: "t_r", Query: "coffee", Mode: "list", Lat: center.Latitude, Lng: center.Longtitude, Zoom: 15, RadiusM: radius,
		}

		jobs, err := runner.CreateJSONLSeedJobs([]runner.SearchTask{task}, jsonlConfig(), nil, nil)
		require.NoError(t, err)

		res, _, err := jobs[0].Process(context.Background(), &scrapemate.Response{Body: body})
		require.NoError(t, err)

		items, ok := res.([]any)
		require.True(t, ok, "jsonl list results are flat, got %T", res)

		return len(items)
	}

	require.Equal(t, 20, run(nil), "no radius: every row kept")
	require.Equal(t, 20, run(floatPtr(0)), "radius 0: every row kept")

	near := run(floatPtr(1))
	require.GreaterOrEqual(t, near, 1)
	require.Less(t, near, 20, "a 1 m radius drops far rows")
}

func TestCreateJSONLSeedJobs_Browser(t *testing.T) {
	t.Parallel()

	tasks := []runner.SearchTask{
		{ID: "t_b1", Query: "techos en Bogotá", Mode: "browser", Lat: 30.2672, Lng: -97.7431, Zoom: 13.6, Lang: "es"},
		{ID: "t_b2", Query: "roofer", Mode: "browser", Lat: 1, Lng: 2, Zoom: 13, Depth: intPtr(3)},
	}

	dedup := deduper.New()
	monitor := exiter.New()

	jobs, err := runner.CreateJSONLSeedJobs(tasks, jsonlConfig(), monitor, dedup)
	require.NoError(t, err)
	require.Len(t, jobs, 2)

	first, ok := jobs[0].(*gmaps.GmapJob)
	require.True(t, ok, "browser tasks become GmapJobs, got %T", jobs[0])
	require.Equal(t, "t_b1", first.ID)
	require.Equal(t, 10, first.MaxDepth, "depth defaults to 10")
	require.Equal(t, "https://www.google.com/maps/search/techos+en+Bogot%C3%A1/@30.267200,-97.743100,14z", first.URL)
	require.Equal(t, "es", first.URLParams["hl"])
	require.NotNil(t, first.Deduper)
	require.NotNil(t, first.ExitMonitor)

	second, ok := jobs[1].(*gmaps.GmapJob)
	require.True(t, ok)
	require.Equal(t, 3, second.MaxDepth)
	require.Equal(t, "en", second.URLParams["hl"])
	require.Equal(t, "https://www.google.com/maps/search/roofer/@1.000000,2.000000,13z", second.URL)
}

func TestCreateJSONLSeedJobs_Empty(t *testing.T) {
	t.Parallel()

	jobs, err := runner.CreateJSONLSeedJobs(nil, jsonlConfig(), nil, nil)
	require.NoError(t, err)
	require.Empty(t, jobs)
}

func TestJSONLBrowserPlaceResultsCarryTaskID(t *testing.T) {
	t.Parallel()

	task := runner.SearchTask{ID: "t_b1", Query: "roofer", Mode: "browser", Lat: 30.2672, Lng: -97.7431, Zoom: 13}

	jobs, err := runner.CreateJSONLSeedJobs([]runner.SearchTask{task}, jsonlConfig(), nil, nil)
	require.NoError(t, err)

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(`<html><body><div role="feed">
		<div jsaction="x"><a href="https://www.google.com/maps/place/a"></a></div>
	</div></body></html>`))
	require.NoError(t, err)

	_, next, err := jobs[0].Process(context.Background(), &scrapemate.Response{Document: doc})
	require.NoError(t, err)
	require.Len(t, next, 1)

	place, ok := next[0].(*gmaps.PlaceJob)
	require.True(t, ok)
	require.Equal(t, "t_b1", place.ParentID, "place jobs carry the task id")

	raw, err := os.ReadFile("../testdata/raw.json")
	require.NoError(t, err)

	result, _, err := place.Process(context.Background(), &scrapemate.Response{Meta: map[string]any{"json": raw}})
	require.NoError(t, err)

	entry, ok := result.(*gmaps.Entry)
	require.True(t, ok)
	require.Equal(t, "t_b1", entry.ID, "input_id of a browser result is the task id")
}

func TestCreateJSONLSeedJobs_MaxPagesAndBaseURL(t *testing.T) {
	t.Parallel()

	cfg := jsonlConfig()
	cfg.MapsBaseURL = "http://127.0.0.1:1/search"

	tasks := []runner.SearchTask{
		{ID: "t_1", Query: "a", Mode: "list", Lat: 1, Lng: 2, Zoom: 10},
		{ID: "t_2", Query: "b", Mode: "list", Lat: 1, Lng: 2, Zoom: 10, MaxPages: intPtr(3)},
	}

	jobs, err := runner.CreateJSONLSeedJobs(tasks, cfg, nil, nil)
	require.NoError(t, err)

	first, ok := jobs[0].(*gmaps.SearchJob)
	require.True(t, ok)
	require.Equal(t, 6, first.MaxPages, "max_pages defaults to 6")
	require.NotNil(t, first.State)
	require.Equal(t, "http://127.0.0.1:1/search", first.URL)

	second, ok := jobs[1].(*gmaps.SearchJob)
	require.True(t, ok)
	require.Equal(t, 3, second.MaxPages)
	require.NotSame(t, first.State, second.State, "each task has its own state")
}

func TestCreateJSONLSeedJobs_SpanAndViewport(t *testing.T) {
	t.Parallel()

	cfg := jsonlConfig()
	cfg.ViewportW, cfg.ViewportH = 800, 600

	tasks := []runner.SearchTask{
		{ID: "t_1", Query: "a", Mode: "list", Lat: 30.2672, Lng: -97.7431, Zoom: 14, SpanM: floatPtr(5000)},
		{ID: "t_2", Query: "b", Mode: "list", Lat: 30.2672, Lng: -97.7431, Zoom: 13},
	}

	jobs, err := runner.CreateJSONLSeedJobs(tasks, cfg, nil, nil)
	require.NoError(t, err)

	first, ok := jobs[0].(*gmaps.SearchJob)
	require.True(t, ok)
	require.Contains(t, first.URLParams["pb"], "!1d21773.27")
	require.Contains(t, first.URLParams["pb"], "!1i800!2i600")
	require.Contains(t, first.URLParams["pb"], "!4f13.1")

	second, ok := jobs[1].(*gmaps.SearchJob)
	require.True(t, ok)
	require.Contains(t, second.URLParams["pb"], "!4f13.1", "no span_m: derived from the zoom")
	require.NotContains(t, second.URLParams["pb"], "!1d3826.902183192154")
}

func TestCreateSeedJobsFastModeRequestUnchanged(t *testing.T) {
	t.Parallel()

	jobs, err := runner.CreateSeedJobs(true, "en", strings.NewReader("coffee\n"), 10, false,
		"30.2672,-97.7431", 15, 10000, nil, nil, false)
	require.NoError(t, err)
	require.Len(t, jobs, 1)

	job, ok := jobs[0].(*gmaps.SearchJob)
	require.True(t, ok)
	require.Empty(t, job.TaskID)

	pb := job.URLParams["pb"]
	require.Contains(t, pb, "!1d3826.902183192154")
	require.Contains(t, pb, "!4f15.0")
	require.Contains(t, pb, "!1i600!2i800")
	require.Contains(t, pb, "!8i0")
}
