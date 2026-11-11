package gmaps

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gosom/scrapemate"
	"github.com/stretchr/testify/require"
)

// statusServer serves status[offset] / body[offset] for every request (offset
// from the pb parameter). A missing offset answers 404.
func statusServer(t *testing.T, replies map[int]struct {
	status int
	body   []byte
},
) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := 0
		if m := offsetPattern.FindStringSubmatch(r.URL.Query().Get("pb")); m != nil {
			offset = atoiOrZero(m[1])
		}

		rep, ok := replies[offset]
		if !ok {
			http.NotFound(w, r)

			return
		}

		w.WriteHeader(rep.status)
		_, _ = w.Write(rep.body)
	}))

	t.Cleanup(srv.Close)

	return srv
}

func atoiOrZero(s string) int {
	n := 0

	for _, c := range s {
		n = n*10 + int(c-'0')
	}

	return n
}

type reply = struct {
	status int
	body   []byte
}

func trackedJob(t *testing.T, baseURL string, maxPages int, env trackerEnv, opts ...SearchJobOptions) *SearchJob {
	t.Helper()

	env.tr.Register("t_x", ModeList, intPtr(maxPages))

	all := append([]SearchJobOptions{WithSearchJobTracker(env.tr)}, opts...)

	return newPagedJob(baseURL, maxPages, all...)
}

func TestSearchJobTracker_CheckResponseAcceptsAnyStatus(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	job := trackedJob(t, "http://127.0.0.1:1/search", 3, env)

	for _, status := range []int{200, 301, 403, 404, 429, 500, 503} {
		require.True(t, job.DoCheckResponse(&scrapemate.Response{StatusCode: status}), "status %d is processed, never retried", status)
	}

	require.False(t, job.DoCheckResponse(&scrapemate.Response{StatusCode: 0, Error: io.EOF}), "no HTTP status is retried")
}

func TestSearchJobTracker_MaxRetriesIsTwo(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	job := trackedJob(t, "http://127.0.0.1:1/search", 3, env)

	require.Equal(t, 2, job.GetMaxRetries())
	require.True(t, job.ProcessOnFetchError())
}

func TestSearchJobTracker_ProcessOrderOnBlockedPage(t *testing.T) {
	t.Parallel()

	rawDir := t.TempDir()
	env := newTrackerEnv(t)
	body := []byte("<html>unusual traffic</html>")

	srv := statusServer(t, map[int]reply{0: {status: 429, body: body}})
	exit := &recordingExiter{}

	job := trackedJob(t, srv.URL, 6, env, WithSearchJobRawDir(rawDir), WithSearchJobExitMonitor(exit))
	res, children, err := job.Process(context.Background(), fetchJob(t, job))

	require.NoError(t, err)
	require.Empty(t, children, "a blocked page schedules no next page")
	require.Empty(t, flattenResult(res))
	require.Equal(t, 1, exit.seedsCompleted, "the task's seed is completed once")
	require.Zero(t, exit.placesFound)

	// the blocked body was captured before anything else happened
	f, openErr := os.Open(filepath.Join(rawDir, "t_x_p0.json.gz"))
	require.NoError(t, openErr)

	defer f.Close()

	zr, zerr := gzip.NewReader(f)
	require.NoError(t, zerr)

	got, rerr := io.ReadAll(zr)
	require.NoError(t, rerr)
	require.Equal(t, body, got)

	line := env.lines(t)[0]
	require.Equal(t, "blocked", line["status"])
	require.Equal(t, "http_429", line["block_reason"])
	require.EqualValues(t, len(body), line["bytes_in"])
}

func TestSearchJobTracker_FullPageSchedulesNext(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	full := outputPage(t)
	short := trimRows(t, full)

	srv := statusServer(t, map[int]reply{0: {status: 200, body: full}, 20: {status: 200, body: short}})
	job := trackedJob(t, srv.URL, 3, env)

	res, children, err := job.Process(context.Background(), fetchJob(t, job))
	require.NoError(t, err)
	require.Len(t, children, 1, "a full page schedules the next one")
	require.Len(t, flattenResult(res), 20)
	require.Empty(t, env.lines(t), "the task is not finished yet, so no line")

	next, ok := children[0].(*SearchJob)
	require.True(t, ok)

	res, children, err = next.Process(context.Background(), fetchJob(t, next))
	require.NoError(t, err)
	require.Empty(t, children)
	require.Empty(t, flattenResult(res), "page 1 repeats page 0 rows after trimming, all duplicates")

	line := env.lines(t)[0]
	require.Equal(t, "ok", line["status"])
	require.EqualValues(t, 2, line["pages_requested"])
	require.EqualValues(t, 25, line["rows_seen"])
	require.EqualValues(t, 20, line["results"])
	require.Equal(t, false, line["last_page_full"])
}

func TestSearchJobTracker_LaterPageBlockedIsPartial(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	full := outputPage(t)

	srv := statusServer(t, map[int]reply{0: {status: 200, body: full}, 20: {status: 503, body: []byte("busy")}})
	job := trackedJob(t, srv.URL, 3, env)

	_, children, err := job.Process(context.Background(), fetchJob(t, job))
	require.NoError(t, err)
	require.Len(t, children, 1)

	next, ok := children[0].(*SearchJob)
	require.True(t, ok)

	_, children, err = next.Process(context.Background(), fetchJob(t, next))
	require.NoError(t, err)
	require.Empty(t, children, "no further page after a block")

	line := env.lines(t)[0]
	require.Equal(t, "partial", line["status"])
	require.Equal(t, "http_503", line["block_reason"])
}

func TestSearchJobTracker_NetworkFailureAndCancellation(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	job := trackedJob(t, "http://127.0.0.1:1/search", 3, env)

	_, children, err := job.Process(context.Background(), &scrapemate.Response{Error: io.ErrUnexpectedEOF})
	require.NoError(t, err)
	require.Empty(t, children)
	require.Equal(t, "error", env.lines(t)[0]["status"])

	// a status-0 response while the run is being cancelled is not a task error:
	// the task is left for FinalizeAll to report as interrupted
	env2 := newTrackerEnv(t)
	job2 := trackedJob(t, "http://127.0.0.1:1/search", 3, env2)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err = job2.Process(ctx, &scrapemate.Response{Error: context.Canceled})
	require.NoError(t, err)
	require.Empty(t, env2.lines(t))
}

func TestSearchJobTracker_EmptyAndGarbageBodies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		body   string
		status string
	}{
		{"valid prefix and no rows (AS-5)", ")]}'\n[[null,null]]", "empty"},
		{"prefix and garbage (AS-6)", ")]}'\nnot json{", "parse_error"},
		{"prefix and odd structure", ")]}'\n[1]", "parse_error"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env := newTrackerEnv(t)
			srv := statusServer(t, map[int]reply{0: {status: 200, body: []byte(tc.body)}})
			job := trackedJob(t, srv.URL, 3, env)

			_, children, err := job.Process(context.Background(), fetchJob(t, job))
			require.NoError(t, err)
			require.Empty(t, children)
			require.Equal(t, tc.status, env.lines(t)[0]["status"])
		})
	}
}

func TestSearchJobTracker_GetFullURLMarksStart(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	job := trackedJob(t, "http://127.0.0.1:1/search", 3, env)

	first := job.GetFullURL()
	require.Contains(t, first, "/search")
	require.NotEmpty(t, env.tr.startedAt("t_x"), "the first call records the start of page 0")

	again := env.tr.startedAt("t_x")

	job.GetFullURL()
	require.Equal(t, again, env.tr.startedAt("t_x"), "later calls do not move it")

	_, err := url.Parse(first)
	require.NoError(t, err)
}

func TestSearchJobTracker_NextPageKeepsTracker(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	job := trackedJob(t, "http://127.0.0.1:1/search", 3, env)
	next := job.nextPage()

	require.Same(t, job.Tracker, next.Tracker)
	require.Equal(t, 2, next.GetMaxRetries())
	require.True(t, next.DoCheckResponse(&scrapemate.Response{StatusCode: 429}))
}

// guard against a data race on the shared writer when pages of different tasks finish together
func TestSearchJobTracker_ConcurrentTasks(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	short := trimRows(t, outputPage(t))
	srv := statusServer(t, map[int]reply{0: {status: 200, body: short}})

	var wg sync.WaitGroup

	for _, id := range []string{"c1", "c2", "c3", "c4"} {
		env.tr.Register(id, ModeList, intPtr(1))

		wg.Add(1)

		go func() {
			defer wg.Done()

			job := NewSearchJob(&MapSearchParams{Query: "coffee", Hl: "en", Location: MapLocation{Radius: everywhere}},
				WithSearchJobTaskID(id), WithSearchJobMaxPages(1), WithSearchJobBaseURL(srv.URL), WithSearchJobTracker(env.tr))
			_, _, _ = job.Process(context.Background(), fetchJob(t, job))
		}()
	}

	wg.Wait()
	require.Len(t, env.lines(t), 4)
}
