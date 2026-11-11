package gmaps

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// contractKeys are the keys of a search report line, docs/contracts.md §4.
var contractKeys = []string{
	"contract", "id", "mode", "status", "block_reason", "error", "max_pages",
	"pages_requested", "pages_ok", "last_page_full", "rows_seen", "results",
	"http_statuses", "bytes_in", "links_found", "links_new", "places_ok",
	"places_failed", "started_at", "finished_at", "duration_ms", "engine_version",
}

// readReport decodes every line of a report file.
func readReport(t *testing.T, path string) []map[string]any {
	t.Helper()

	f, err := os.Open(path)
	require.NoError(t, err)

	defer f.Close()

	var lines []map[string]any

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)

	for sc.Scan() {
		var m map[string]any

		require.NoError(t, json.Unmarshal(sc.Bytes(), &m), "line %q", sc.Text())

		lines = append(lines, m)
	}

	require.NoError(t, sc.Err())

	return lines
}

// fakeClock returns a clock that advances by step on every call.
func fakeClock(start time.Time, step time.Duration) func() time.Time {
	var (
		mu  sync.Mutex
		cur = start
	)

	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()

		now := cur
		cur = cur.Add(step)

		return now
	}
}

type trackerEnv struct {
	tr   *TaskTracker
	path string
}

func newTrackerEnv(t *testing.T) trackerEnv {
	t.Helper()

	path := filepath.Join(t.TempDir(), "searches.jsonl")

	w, err := NewReportWriter(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })

	start := time.Date(2026, 11, 20, 14, 3, 11, 204*int(time.Millisecond), time.UTC)

	return trackerEnv{tr: NewTaskTracker(w, "v1.18.1-gmx.2", fakeClock(start, 100*time.Millisecond)), path: path}
}

func (e trackerEnv) lines(t *testing.T) []map[string]any {
	t.Helper()

	return readReport(t, e.path)
}

func okPage(page, rows, results int) PageResult {
	return PageResult{Page: page, Status: 200, Bytes: 1000 + page, Rows: rows, Results: results, Outcome: PageOK}
}

func TestSearchReportKeySet(t *testing.T) {
	t.Parallel()

	reports := map[string]SearchReport{
		"minimal": {HTTPStatuses: []int{}},
		"full": {
			Contract: ContractVersion, ID: "t1", Mode: "list", Status: StatusPartial,
			BlockReason: strPtr("http_503"), Error: strPtr("x"), MaxPages: intPtr(6),
			PagesRequested: 3, PagesOK: 2, LastPageFull: true, RowsSeen: 40, Results: 40,
			HTTPStatuses: []int{200, 200, 503}, BytesIn: intPtr(10), LinksFound: intPtr(1),
			LinksNew: intPtr(1), PlacesOK: intPtr(1), PlacesFailed: intPtr(0),
			StartedAt: "a", FinishedAt: "b", DurationMS: 5, EngineVersion: "v",
		},
	}

	for name, rep := range reports {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			raw, err := json.Marshal(rep)
			require.NoError(t, err)

			var m map[string]any

			require.NoError(t, json.Unmarshal(raw, &m))

			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}

			require.ElementsMatch(t, contractKeys, keys)
			require.Len(t, keys, 22)

			if name == "minimal" {
				for _, k := range []string{"block_reason", "error", "max_pages", "bytes_in", "links_found", "links_new", "places_ok", "places_failed"} {
					require.Nil(t, m[k], k)
				}

				require.Equal(t, []any{}, m["http_statuses"], "an empty list is [] not null")
			}
		})
	}
}

func TestReportWriterOneLinePerID(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "r.jsonl")

	w, err := NewReportWriter(path)
	require.NoError(t, err)

	defer w.Close()

	require.NoError(t, w.Write(SearchReport{ID: "a", Status: StatusOK}))
	require.Error(t, w.Write(SearchReport{ID: "a", Status: StatusError}), "second line for the same id")
	require.NoError(t, w.Write(SearchReport{ID: "b", Status: StatusEmpty}))

	lines := readReport(t, path)
	require.Len(t, lines, 2)
	require.Equal(t, "ok", lines[0]["status"], "the first line stays")
}

func TestReportWriterFlushesEachLine(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "r.jsonl")

	w, err := NewReportWriter(path)
	require.NoError(t, err)

	defer w.Close()

	for i := range 3 {
		require.NoError(t, w.Write(SearchReport{ID: fmt.Sprintf("t%d", i), Status: StatusOK}))
		require.Len(t, readReport(t, path), i+1, "the line is readable before Close")
	}
}

func TestReportWriterConcurrent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "r.jsonl")

	w, err := NewReportWriter(path)
	require.NoError(t, err)

	defer w.Close()

	var wg sync.WaitGroup

	for i := range 50 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			assertNoError(t, w.Write(SearchReport{ID: fmt.Sprintf("t%02d", i), Status: StatusOK}))
		}()
	}

	wg.Wait()

	lines := readReport(t, path)
	require.Len(t, lines, 50)

	seen := map[string]bool{}
	for _, l := range lines {
		seen[l["id"].(string)] = true //nolint:errcheck,forcetypeassert // test data
	}

	require.Len(t, seen, 50)
}

func assertNoError(t *testing.T, err error) {
	t.Helper()

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestReportWriterNilIsNoop(t *testing.T) {
	t.Parallel()

	var w *ReportWriter

	require.NoError(t, w.Write(SearchReport{ID: "a"}))
	require.NoError(t, w.Err())
	require.NoError(t, w.Close())
}

func TestReportWriterCreateFailure(t *testing.T) {
	t.Parallel()

	_, err := NewReportWriter(filepath.Join(t.TempDir(), "missing", "r.jsonl"))
	require.Error(t, err)
}

func TestReportWriterReplacesExistingFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "r.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("{\"old\":1}\n{\"old\":2}\n"), 0o600))

	w, err := NewReportWriter(path)
	require.NoError(t, err)

	defer w.Close()

	require.NoError(t, w.Write(SearchReport{ID: "a", Status: StatusOK}))
	require.Len(t, readReport(t, path), 1)
}

func TestSanitizeError(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("é", 700)

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"credentials", "dial http://user:secret@host:1: refused", "dial http://***@host:1: refused"},
		{"two urls", "a http://u:p@h1 b socks5://x:y@h2:9", "a http://***@h1 b socks5://***@h2:9"},
		{"no credentials", "plain text", "plain text"},
		{"newlines and tabs", "line one\nline two\tend\r\n", "line one line two end  "},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, SanitizeError(tc.in))
		})
	}

	got := SanitizeError(long)
	require.Len(t, []rune(got), 500, "truncated to 500 runes, multi-byte safe")
}

func TestTrackerMasksErrorInReport(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	env.tr.Register("t1", ModeList, intPtr(6))
	env.tr.FetchStarted("t1", 0)
	env.tr.PageDone("t1", PageResult{Page: 0, Status: 0, Outcome: PageNetworkError, Err: "dial http://user:secret@host:1: refused\nsecond line"})
	env.tr.Finish("t1")

	raw, err := os.ReadFile(env.path)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret")

	lines := env.lines(t)
	require.Equal(t, "dial http://***@host:1: refused second line", lines[0]["error"])
}

func TestComputeStatus(t *testing.T) {
	t.Parallel()

	type want struct {
		status, reason, errText                    string
		pagesRequested, pagesOK, rowsSeen, results int
		lastPageFull                               bool
		statuses                                   []int
	}

	tests := []struct {
		name  string
		steps func(tr *TaskTracker)
		want  want
	}{
		{
			name: "page 0 blocked http_429",
			steps: func(tr *TaskTracker) {
				tr.PageDone("t1", PageResult{Page: 0, Status: 429, Bytes: 50, Outcome: PageBlocked, Reason: ReasonHTTP429})
				tr.Finish("t1")
			},
			want: want{status: "blocked", reason: "http_429", pagesRequested: 1, statuses: []int{429}},
		},
		{
			name: "page 0 blocked no_xssi_prefix",
			steps: func(tr *TaskTracker) {
				tr.PageDone("t1", PageResult{Page: 0, Status: 200, Outcome: PageBlocked, Reason: ReasonNoXSSIPrefix})
				tr.Finish("t1")
			},
			want: want{status: "blocked", reason: "no_xssi_prefix", pagesRequested: 1, statuses: []int{200}},
		},
		{
			name: "page 0 http 403 is a page error",
			steps: func(tr *TaskTracker) {
				tr.PageDone("t1", PageResult{Page: 0, Status: 403, Outcome: PageHTTPError, Err: "http status 403"})
				tr.Finish("t1")
			},
			want: want{status: "error", errText: "http status 403", pagesRequested: 1, statuses: []int{403}},
		},
		{
			name: "page 0 network failure",
			steps: func(tr *TaskTracker) {
				tr.PageDone("t1", PageResult{Page: 0, Status: 0, Outcome: PageNetworkError, Err: "EOF"})
				tr.Finish("t1")
			},
			want: want{status: "error", errText: "EOF", pagesRequested: 1, statuses: []int{0}},
		},
		{
			name: "page 0 parse error",
			steps: func(tr *TaskTracker) {
				tr.PageDone("t1", PageResult{Page: 0, Status: 200, Outcome: PageParseError, Err: "failed to parse search results: x"})
				tr.Finish("t1")
			},
			want: want{status: "parse_error", errText: "failed to parse search results: x", pagesRequested: 1, statuses: []int{200}},
		},
		{
			name: "page 0 ok with no rows is empty",
			steps: func(tr *TaskTracker) {
				tr.PageDone("t1", okPage(0, 0, 0))
				tr.Finish("t1")
			},
			want: want{status: "empty", pagesRequested: 1, pagesOK: 1, statuses: []int{200}},
		},
		{
			name: "rows all removed by the radius is empty",
			steps: func(tr *TaskTracker) {
				tr.PageDone("t1", okPage(0, 20, 0))
				tr.Finish("t1")
			},
			want: want{status: "empty", pagesRequested: 1, pagesOK: 1, rowsSeen: 20, lastPageFull: true, statuses: []int{200}},
		},
		{
			name: "ok on a short page",
			steps: func(tr *TaskTracker) {
				tr.PageDone("t1", okPage(0, 5, 5))
				tr.Finish("t1")
			},
			want: want{status: "ok", pagesRequested: 1, pagesOK: 1, rowsSeen: 5, results: 5, statuses: []int{200}},
		},
		{
			name: "partial after a 503 on page 2 (AS-4)",
			steps: func(tr *TaskTracker) {
				tr.PageDone("t1", okPage(0, 20, 20))
				tr.PageDone("t1", okPage(1, 20, 20))
				tr.PageDone("t1", PageResult{Page: 2, Status: 503, Bytes: 20, Outcome: PageBlocked, Reason: ReasonHTTP503})
				tr.Finish("t1")
			},
			want: want{status: "partial", reason: "http_503", pagesRequested: 3, pagesOK: 2, rowsSeen: 40, results: 40, lastPageFull: true, statuses: []int{200, 200, 503}},
		},
		{
			name: "partial after a network failure on page 1",
			steps: func(tr *TaskTracker) {
				tr.PageDone("t1", okPage(0, 20, 20))
				tr.PageDone("t1", PageResult{Page: 1, Status: 0, Outcome: PageNetworkError, Err: "timeout"})
				tr.Finish("t1")
			},
			want: want{status: "partial", errText: "timeout", pagesRequested: 2, pagesOK: 1, rowsSeen: 20, results: 20, lastPageFull: true, statuses: []int{200, 0}},
		},
		{
			name: "partial after a parse error on page 1",
			steps: func(tr *TaskTracker) {
				tr.PageDone("t1", okPage(0, 20, 20))
				tr.PageDone("t1", PageResult{Page: 1, Status: 200, Outcome: PageParseError, Err: "bad"})
				tr.Finish("t1")
			},
			want: want{status: "partial", errText: "bad", pagesRequested: 2, pagesOK: 1, rowsSeen: 20, results: 20, lastPageFull: true, statuses: []int{200, 200}},
		},
		{
			name: "cut off after page 0 is partial",
			steps: func(tr *TaskTracker) {
				tr.FetchStarted("t1", 0)
				tr.PageDone("t1", okPage(0, 20, 20))
				tr.FinalizeAll(ErrInterrupted)
			},
			want: want{status: "partial", errText: "interrupted", pagesRequested: 1, pagesOK: 1, rowsSeen: 20, results: 20, lastPageFull: true, statuses: []int{200}},
		},
		{
			name: "cut off with no successful page is an error",
			steps: func(tr *TaskTracker) {
				tr.FetchStarted("t1", 0)
				tr.FinalizeAll(ErrAborted)
			},
			want: want{status: "error", errText: "aborted: max-blocked", statuses: []int{}},
		},
		{
			name: "never started is skipped",
			steps: func(tr *TaskTracker) {
				tr.FinalizeAll(ErrAborted)
			},
			want: want{status: "skipped", errText: "aborted: max-blocked", statuses: []int{}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env := newTrackerEnv(t)
			env.tr.Register("t1", ModeList, intPtr(6))
			tc.steps(env.tr)

			lines := env.lines(t)
			require.Len(t, lines, 1)

			line := lines[0]
			require.Equal(t, tc.want.status, line["status"])
			require.EqualValues(t, tc.want.pagesRequested, line["pages_requested"])
			require.EqualValues(t, tc.want.pagesOK, line["pages_ok"])
			require.EqualValues(t, tc.want.rowsSeen, line["rows_seen"])
			require.EqualValues(t, tc.want.results, line["results"])
			require.Equal(t, tc.want.lastPageFull, line["last_page_full"])

			if tc.want.reason == "" {
				require.Nil(t, line["block_reason"])
			} else {
				require.Equal(t, tc.want.reason, line["block_reason"])
			}

			if tc.want.errText == "" {
				require.Nil(t, line["error"])
			} else {
				require.Equal(t, tc.want.errText, line["error"])
			}

			rawStatuses := line["http_statuses"].([]any) //nolint:errcheck,forcetypeassert // test data
			got := make([]int, 0, len(rawStatuses))

			for _, v := range rawStatuses {
				got = append(got, int(v.(float64))) //nolint:errcheck,forcetypeassert // test data
			}

			require.Equal(t, tc.want.statuses, got)
		})
	}
}

func TestTrackerFinalizeAll(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	for _, id := range []string{"a", "b", "c"} {
		env.tr.Register(id, ModeList, intPtr(6))
	}

	env.tr.PageDone("a", okPage(0, 5, 5))
	env.tr.Finish("a")

	env.tr.FinalizeAll(ErrInterrupted)
	env.tr.FinalizeAll(ErrInterrupted)

	lines := env.lines(t)
	require.Len(t, lines, 3)

	byID := map[string]string{}
	for _, l := range lines {
		byID[l["id"].(string)] = l["status"].(string) //nolint:errcheck,forcetypeassert // test data
	}

	require.Equal(t, map[string]string{"a": "ok", "b": "skipped", "c": "skipped"}, byID)
}

func TestTrackerSkippedLineShape(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	env.tr.Register("b", ModeList, intPtr(4))
	env.tr.Register("w", ModeBrowser, nil)
	env.tr.FinalizeAll(ErrAborted)

	for _, l := range env.lines(t) {
		require.Equal(t, "skipped", l["status"])
		require.Equal(t, "aborted: max-blocked", l["error"])
		require.Nil(t, l["block_reason"])
		require.EqualValues(t, 0, l["pages_requested"])
		require.EqualValues(t, 0, l["pages_ok"])
		require.Equal(t, false, l["last_page_full"])
		require.EqualValues(t, 0, l["rows_seen"])
		require.EqualValues(t, 0, l["results"])
		require.Equal(t, []any{}, l["http_statuses"])
		require.Equal(t, l["started_at"], l["finished_at"])
		require.EqualValues(t, 0, l["duration_ms"])
		require.Equal(t, "v1.18.1-gmx.2", l["engine_version"])
		require.Equal(t, ContractVersion, l["contract"])

		if l["id"] == "b" {
			require.EqualValues(t, 0, l["bytes_in"])
			require.EqualValues(t, 4, l["max_pages"])
			require.Nil(t, l["links_found"])
		} else {
			require.Nil(t, l["bytes_in"], "browser lines have no bytes_in")
			require.Nil(t, l["max_pages"])
			require.EqualValues(t, 0, l["links_found"])
		}
	}
}

func TestTrackerFinishIsIdempotent(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	env.tr.Register("a", ModeList, intPtr(6))
	env.tr.PageDone("a", okPage(0, 5, 5))
	env.tr.Finish("a")
	env.tr.Finish("a")
	env.tr.PageDone("a", okPage(1, 5, 5))
	env.tr.Finish("a")

	lines := env.lines(t)
	require.Len(t, lines, 1)
	require.EqualValues(t, 1, lines[0]["pages_requested"], "late events after the line was written are ignored")
}

func TestTrackerUnknownIDIsIgnored(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	env.tr.FetchStarted("ghost", 0)
	env.tr.PageDone("ghost", okPage(0, 1, 1))
	env.tr.Finish("ghost")
	env.tr.FinalizeAll(ErrInterrupted)

	require.Empty(t, env.lines(t))
}

func TestTrackerTimestamps(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t) // clock: 14:03:11.204Z, +100 ms per call
	env.tr.Register("a", ModeList, intPtr(6))
	env.tr.FetchStarted("a", 0) // first call: start = 14:03:11.204
	env.tr.FetchStarted("a", 0) // does not move the start
	env.tr.FetchStarted("a", 1) // later pages do not move it either
	env.tr.PageDone("a", okPage(0, 5, 5))
	env.tr.Finish("a")

	line := env.lines(t)[0]
	require.Equal(t, "2026-11-20T14:03:11.204Z", line["started_at"])

	finished, err := time.Parse("2006-01-02T15:04:05.000Z", line["finished_at"].(string)) //nolint:errcheck,forcetypeassert // test data
	require.NoError(t, err)

	started, err := time.Parse("2006-01-02T15:04:05.000Z", line["started_at"].(string)) //nolint:errcheck,forcetypeassert // test data
	require.NoError(t, err)

	require.EqualValues(t, finished.Sub(started).Milliseconds(), line["duration_ms"])
	require.Positive(t, line["duration_ms"])
}

func TestTrackerCounts(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	for _, id := range []string{"a", "b", "c", "d"} {
		env.tr.Register(id, ModeList, intPtr(6))
	}

	env.tr.PageDone("a", okPage(0, 5, 5))
	env.tr.Finish("a")
	env.tr.PageDone("b", PageResult{Page: 0, Status: 429, Outcome: PageBlocked, Reason: ReasonHTTP429})
	env.tr.Finish("b")
	env.tr.FinalizeAll(ErrInterrupted)

	require.Equal(t, Summary{Tasks: 4, OK: 1, Blocked: 1, Skipped: 2}, env.tr.Counts())
}

func BenchmarkReportWrite(b *testing.B) {
	benchmarkReportWrite(b, false)
}

func BenchmarkReportWriteSync(b *testing.B) {
	benchmarkReportWrite(b, true)
}

func benchmarkReportWrite(b *testing.B, fsync bool) {
	b.Helper()

	w, err := NewReportWriter(filepath.Join(b.TempDir(), "r.jsonl"))
	if err != nil {
		b.Fatal(err)
	}

	defer w.Close()

	w.syncEachLine = fsync

	rep := SearchReport{
		Contract: ContractVersion, ID: "t", Mode: "list", Status: StatusOK, MaxPages: intPtr(6),
		PagesRequested: 4, PagesOK: 4, RowsSeen: 71, Results: 69, HTTPStatuses: []int{200, 200, 200, 200},
		BytesIn: intPtr(402113), StartedAt: "2026-11-20T14:03:11.204Z", FinishedAt: "2026-11-20T14:03:19.871Z",
		DurationMS: 8667, EngineVersion: "v1.18.1-gmx.2",
	}

	b.ResetTimer()

	for i := range b.N {
		rep.ID = fmt.Sprintf("t%d", i)

		if err := w.Write(rep); err != nil {
			b.Fatal(err)
		}
	}
}

func TestReportWriterRecordsFirstWriteFailure(t *testing.T) {
	t.Parallel()

	w, err := NewReportWriter(filepath.Join(t.TempDir(), "r.jsonl"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	first := w.Write(SearchReport{ID: "a"})
	require.Error(t, first, "writing to a closed file fails")
	require.Equal(t, first, w.Err(), "the first failure is remembered")

	require.Error(t, w.Write(SearchReport{ID: "b"}))
	require.Equal(t, first, w.Err(), "and not replaced by later ones")
}

func TestNewTaskTrackerDefaults(t *testing.T) {
	t.Parallel()

	tr := NewTaskTracker(nil, "v", nil) // no writer, real clock
	tr.Register("a", ModeList, intPtr(1))
	tr.Register("a", ModeList, intPtr(9)) // registering twice keeps the first
	tr.PageDone("a", okPage(0, 3, 3))
	tr.Finish("a")

	require.Equal(t, Summary{Tasks: 1, OK: 1}, tr.Counts())
}
