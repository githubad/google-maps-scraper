package gmaps

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"sync"
	"testing"

	"github.com/gosom/scrapemate"
	"github.com/stretchr/testify/require"
)

// Test helpers for the list-mode pagination tests: a local httptest "Google",
// a driver that runs a task page by page the way scrapemate would, and a
// recording exit monitor. Nothing here talks to the network beyond loopback.

var offsetPattern = regexp.MustCompile(`!8i(\d+)`)

func loadListmodeFixture(t *testing.T, name string) []byte {
	t.Helper()

	raw, err := os.ReadFile("../testdata/listmode/" + name)
	require.NoError(t, err)

	return raw
}

func dropPrefixLine(b []byte) []byte {
	if bytes.HasPrefix(b, []byte(")]}'")) {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			return b[i+1:]
		}
	}

	return b
}

// parseServed parses a served body (with or without the prefix line).
func parseServed(t *testing.T, body []byte) []*Entry {
	t.Helper()

	entries, err := ParseSearchResults(dropPrefixLine(body))
	require.NoError(t, err)

	return entries
}

// trimRows keeps the first n rows of a body in whichever layout it uses and
// returns it with the prefix line.
func trimRows(t *testing.T, body []byte) []byte {
	t.Helper()

	const n = 5 // every test trims to a short page of five rows

	var data []any

	require.NoError(t, json.Unmarshal(dropPrefixLine(body), &data))

	if container, ok := data[0].([]any); ok {
		if items, isList := at(container, 1).([]any); isList && len(items) >= 2 {
			container[1] = items[:1+n]
		} else if list, isChrome := at(data, 64).([]any); isChrome {
			data[64] = list[:n]
		}
	}

	out, err := json.Marshal(data)
	require.NoError(t, err)

	return withPrefix(out)
}

type hitLog struct {
	mu      sync.Mutex
	offsets []int
	queries []string
	langs   []string
}

func (h *hitLog) record(offset int, q, hl string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.offsets = append(h.offsets, offset)
	h.queries = append(h.queries, q)
	h.langs = append(h.langs, hl)
}

func (h *hitLog) Offsets() []int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]int(nil), h.offsets...)
}

func (h *hitLog) Queries() []string {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]string(nil), h.queries...)
}

func (h *hitLog) Langs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]string(nil), h.langs...)
}

// newBodyServer serves byOffset(offset) for every request; the offset is read
// from the pb parameter's !8i token (absent means 0). A nil body is a 404.
func newBodyServer(t *testing.T, byOffset func(offset int) []byte) (*httptest.Server, *hitLog) {
	t.Helper()

	log := &hitLog{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		offset := 0
		if m := offsetPattern.FindStringSubmatch(q.Get("pb")); m != nil {
			offset, _ = strconv.Atoi(m[1])
		}

		log.record(offset, q.Get("q"), q.Get("hl"))

		body := byOffset(offset)
		if body == nil {
			http.NotFound(w, r)

			return
		}

		_, _ = w.Write(body)
	}))

	t.Cleanup(srv.Close)

	return srv, log
}

type taskRun struct {
	Entries []*Entry
	Errors  []error
}

// fetchJob performs the job's request the way the stealth fetcher would.
func fetchJob(t *testing.T, j *SearchJob) *scrapemate.Response {
	t.Helper()

	u, err := url.Parse(j.URL)
	require.NoError(t, err)

	q := u.Query()
	for k, v := range j.URLParams {
		q.Set(k, v)
	}

	u.RawQuery = q.Encode()

	httpResp, err := http.Get(u.String()) //nolint:noctx // loopback test server
	if err != nil {
		return &scrapemate.Response{Error: err}
	}

	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
	require.NoError(t, err)

	return &scrapemate.Response{Body: body, StatusCode: httpResp.StatusCode}
}

func flattenResult(res any) []*Entry {
	switch v := res.(type) {
	case []*Entry:
		return v
	case []any:
		out := make([]*Entry, 0, len(v))

		for _, item := range v {
			if e, ok := item.(*Entry); ok {
				out = append(out, e)
			}
		}

		return out
	default:
		return nil
	}
}

// runTask runs job and every job it returns, first in first out, until none is left.
func runTask(t *testing.T, job *SearchJob) taskRun {
	t.Helper()

	var run taskRun

	queue := []scrapemate.IJob{job}

	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]

		sj, ok := next.(*SearchJob)
		require.True(t, ok, "only SearchJobs are expected, got %T", next)

		res, children, err := sj.Process(context.Background(), fetchJob(t, sj))
		if err != nil {
			run.Errors = append(run.Errors, err)
		}

		run.Entries = append(run.Entries, flattenResult(res)...)
		queue = append(queue, children...)
	}

	return run
}

// newPagedJob builds a task job that fetches from baseURL.
func newPagedJob(baseURL string, maxPages int, opts ...SearchJobOptions) *SearchJob {
	all := append([]SearchJobOptions{
		WithSearchJobTaskID("t_x"),
		WithSearchJobMaxPages(maxPages),
		WithSearchJobBaseURL(baseURL),
	}, opts...)

	return NewSearchJob(&MapSearchParams{
		Query:    "coffee",
		Hl:       "en",
		Location: MapLocation{Radius: everywhere},
	}, all...)
}

// recordingExiter is an exiter.Exiter that records the calls it receives.
type recordingExiter struct {
	mu             sync.Mutex
	seedsCompleted int
	placesFound    int
	placesDone     int
	events         []string
}

func (r *recordingExiter) SetSeedCount(int)                 {}
func (r *recordingExiter) SetCancelFunc(context.CancelFunc) {}
func (r *recordingExiter) Run(context.Context)              {}

func (r *recordingExiter) IncrSeedCompleted(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seedsCompleted += n
	r.events = append(r.events, "seed")
}

func (r *recordingExiter) IncrPlacesFound(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.placesFound += n
	r.events = append(r.events, "found")
}

func (r *recordingExiter) IncrPlacesCompleted(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.placesDone += n
	r.events = append(r.events, "done")
}

func ranks(entries []*Entry) []int {
	out := make([]int, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Rank)
	}

	return out
}

func sequence(to int) []int {
	out := make([]int, 0, to)
	for i := 1; i <= to; i++ {
		out = append(out, i)
	}

	return out
}
