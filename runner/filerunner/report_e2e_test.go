package filerunner_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gosom/google-maps-scraper/gmaps"
	"github.com/gosom/google-maps-scraper/runner"
	"github.com/gosom/google-maps-scraper/runner/filerunner"
)

// The tests in this file run the real scrapemate app and the stealth fetcher
// against a local httptest server standing in for Google, as
// TestJSONLEndToEndPagination does. Nothing leaves the machine.

var (
	offsetRe  = regexp.MustCompile(`!8i(\d+)`)
	dataIDRe  = regexp.MustCompile(`0x[0-9a-f]+:0x[0-9a-f]+`)
	placeIDRe = regexp.MustCompile(`ChIJ[A-Za-z0-9_-]{20,}`)
)

// contractKeys are the keys of a search report line, docs/contracts.md §4.
var contractKeys = []string{
	"contract", "id", "mode", "status", "block_reason", "error", "max_pages",
	"pages_requested", "pages_ok", "last_page_full", "rows_seen", "results",
	"http_statuses", "bytes_in", "links_found", "links_new", "places_ok",
	"places_failed", "started_at", "finished_at", "duration_ms", "engine_version",
}

type batchResult struct {
	lines  []map[string]any
	hits   int64
	err    error
	dir    string
	rawDir string
	report string
}

// pageOffset reads the offset (!8i) of a list request.
func pageOffset(r *http.Request) int {
	if m := offsetRe.FindStringSubmatch(r.URL.Query().Get("pb")); m != nil {
		n, _ := strconv.Atoi(m[1])

		return n
	}

	return 0
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "listmode", name))
	require.NoError(t, err)

	return raw
}

// alterPage returns a copy of a served page in which every data_id, place_id
// and title is changed, so that its rows are different businesses.
func alterPage(t *testing.T, body []byte, tag string) []byte {
	t.Helper()

	entries, err := gmaps.ParseSearchResults(bytes.TrimPrefix(body, []byte(")]}'\n")))
	require.NoError(t, err)

	out := dataIDRe.ReplaceAllFunc(body, func(m []byte) []byte { return append(append([]byte{}, m...), []byte(tag)...) })
	out = placeIDRe.ReplaceAllFunc(out, func(m []byte) []byte { return append(append([]byte{}, m...), []byte(tag)...) })

	for _, e := range entries {
		if e.Title != "" && !strings.ContainsAny(e.Title, "\"\\") {
			out = bytes.ReplaceAll(out, []byte(`"`+e.Title+`"`), []byte(`"`+e.Title+" "+tag+`"`))
		}
	}

	require.NotEqual(t, body, out)

	return out
}

// runBatch writes the task lines, serves them from handler and runs the file runner.
func runBatch(t *testing.T, taskLines []string, handler func(w http.ResponseWriter, r *http.Request), mutate func(*runner.Config)) batchResult {
	t.Helper()

	return runBatchCancel(t, taskLines, handler, mutate, 0)
}

// runBatchCancel is runBatch with the run context cancelled after cancelAfter
// (0 = never), the way a signal would end it.
func runBatchCancel(t *testing.T, taskLines []string, handler func(w http.ResponseWriter, r *http.Request), mutate func(*runner.Config), cancelAfter time.Duration) batchResult {
	t.Helper()
	t.Setenv("DISABLE_TELEMETRY", "1")

	var hits atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	input := filepath.Join(dir, "tasks.jsonl")
	require.NoError(t, os.WriteFile(input, []byte(strings.Join(taskLines, "\n")+"\n"), 0o600))

	cfg := testConfig(input, filepath.Join(dir, "results.jsonl"))
	cfg.InputFormat = "jsonl"
	cfg.JSON = true
	cfg.MapsBaseURL = srv.URL
	cfg.Radius = 0
	cfg.RawDir = filepath.Join(dir, "raw")
	cfg.SearchReport = filepath.Join(dir, "searches.jsonl")

	if mutate != nil {
		mutate(cfg)
	}

	res := batchResult{dir: dir, rawDir: cfg.RawDir, report: cfg.SearchReport}

	r, err := filerunner.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	if cancelAfter > 0 {
		timer := time.AfterFunc(cancelAfter, cancel)
		defer timer.Stop()
	}

	res.err = r.Run(ctx)
	if errors.Is(res.err, context.Canceled) {
		res.err = nil // how scrapemate reports a run that ended by itself; main exits 0
	}

	res.hits = hits.Load()

	if cfg.SearchReport != "" && !strings.HasPrefix(cfg.SearchReport, "/dev/") { // /dev/full reads as endless zeros
		res.lines = readReportLines(t, cfg.SearchReport)
	}

	return res
}

func readReportLines(t *testing.T, path string) []map[string]any {
	t.Helper()

	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}

	require.NoError(t, err)

	var lines []map[string]any

	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if l == "" {
			continue
		}

		var m map[string]any

		require.NoError(t, json.Unmarshal([]byte(l), &m), "line %q", l)

		lines = append(lines, m)
	}

	return lines
}

func taskLine(id, query string) string {
	return `{"id":"` + id + `","query":"` + query + `","lat":30.2672,"lng":-97.7431,"zoom":14,"max_pages":6}`
}

func intsOf(t *testing.T, v any) []int {
	t.Helper()

	list, ok := v.([]any)
	require.True(t, ok, "http_statuses must be a JSON array")

	out := make([]int, 0, len(list))

	for _, item := range list {
		f, isNum := item.(float64)
		require.True(t, isNum)

		out = append(out, int(f))
	}

	return out
}

func TestSearchReportE2E_AS1_Blocked429NoRetry(t *testing.T) {
	res := runBatch(t, []string{taskLine("t_1", "roofer")}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("<html>unusual traffic</html>"))
	}, nil)

	require.NoError(t, res.err)
	require.EqualValues(t, 1, res.hits, "exactly one request: no retry of a block")
	require.Len(t, res.lines, 1)
	require.Equal(t, "blocked", res.lines[0]["status"])
	require.Equal(t, "http_429", res.lines[0]["block_reason"], "429 wins over the unusual traffic text")
	require.Equal(t, []int{429}, intsOf(t, res.lines[0]["http_statuses"]))
}

func TestSearchReportE2E_AS2_Captcha(t *testing.T) {
	res := runBatch(t, []string{taskLine("t_1", "roofer")}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><div class="g-recaptcha"></div></html>`))
	}, nil)

	require.NoError(t, res.err)
	require.EqualValues(t, 1, res.hits)
	require.Equal(t, "blocked", res.lines[0]["status"])
	require.Equal(t, "captcha_page", res.lines[0]["block_reason"])
}

func TestSearchReportE2E_AS3_NoPrefix(t *testing.T) {
	res := runBatch(t, []string{taskLine("t_1", "roofer")}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"a":1}`))
	}, nil)

	require.NoError(t, res.err)
	require.Equal(t, "blocked", res.lines[0]["status"])
	require.Equal(t, "no_xssi_prefix", res.lines[0]["block_reason"])
}

func TestSearchReportE2E_AS4_PartialAfter503(t *testing.T) {
	p0 := readFixture(t, "har_01_roofing-contractor_o0.json")
	p1 := alterPage(t, readFixture(t, "har_02_roofing-contractor_o20.json"), "x")
	busy := []byte("busy")

	res := runBatch(t, []string{taskLine("t_1", "roofer")}, func(w http.ResponseWriter, r *http.Request) {
		switch pageOffset(r) {
		case 0:
			_, _ = w.Write(p0)
		case 20:
			_, _ = w.Write(p1)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write(busy)
		}
	}, nil)

	require.NoError(t, res.err)
	require.Len(t, res.lines, 1)

	line := res.lines[0]
	require.Equal(t, "partial", line["status"])
	require.Equal(t, "http_503", line["block_reason"])
	require.EqualValues(t, 3, line["pages_requested"])
	require.EqualValues(t, 2, line["pages_ok"])
	require.EqualValues(t, 40, line["results"])
	require.EqualValues(t, 40, line["rows_seen"])
	require.Equal(t, true, line["last_page_full"])
	require.Equal(t, []int{200, 200, 503}, intsOf(t, line["http_statuses"]))
	require.EqualValues(t, len(p0)+len(p1)+len(busy), line["bytes_in"])
	require.EqualValues(t, 3, res.hits, "no page after the block")
}

func TestSearchReportE2E_AS5_Empty(t *testing.T) {
	res := runBatch(t, []string{taskLine("t_1", "roofer")}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(")]}'\n[[null,null]]"))
	}, nil)

	require.NoError(t, res.err)
	require.Equal(t, "empty", res.lines[0]["status"])
	require.EqualValues(t, 0, res.lines[0]["results"])
}

func TestSearchReportE2E_AS6_ParseError(t *testing.T) {
	res := runBatch(t, []string{taskLine("t_1", "roofer")}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(")]}'\nnot json{"))
	}, nil)

	require.NoError(t, res.err)
	require.Equal(t, "parse_error", res.lines[0]["status"])
	require.NotNil(t, res.lines[0]["error"])
}

func TestSearchReportE2E_AS7_NetworkErrorThreeAttempts(t *testing.T) {
	res := runBatch(t, []string{taskLine("t_1", "roofer")}, func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}

		conn, _, err := hj.Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}, nil)

	require.NoError(t, res.err)
	require.EqualValues(t, 3, res.hits, "1 try + 2 retries")
	require.Equal(t, "error", res.lines[0]["status"])
	require.Equal(t, []int{0}, intsOf(t, res.lines[0]["http_statuses"]))
	require.NotNil(t, res.lines[0]["error"])
}

func TestSearchReportE2E_AS19_RadiusFiltersEverything(t *testing.T) {
	p0 := readFixture(t, "har_01_roofing-contractor_o0.json")

	res := runBatch(t, []string{`{"id":"t_1","query":"roofer","lat":0,"lng":0,"zoom":14,"max_pages":1,"radius_m":1}`},
		func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(p0) }, nil)

	require.NoError(t, res.err)

	line := res.lines[0]
	require.Equal(t, "empty", line["status"])
	require.EqualValues(t, 20, line["rows_seen"])
	require.EqualValues(t, 0, line["results"])
}

func gunzipFile(t *testing.T, path string) []byte {
	t.Helper()

	f, err := os.Open(path)
	require.NoError(t, err)

	defer f.Close()

	zr, err := gzip.NewReader(f)
	require.NoError(t, err)

	out, err := io.ReadAll(zr)
	require.NoError(t, err)

	return out
}

func TestSearchReportE2E_AS20_RawBlockedBody(t *testing.T) {
	blocked := []byte("<html>unusual traffic</html>")
	forbidden := []byte("forbidden")

	res := runBatch(t, []string{taskLine("t_429", "blockme"), taskLine("t_403", "forbidme")},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("q") == "blockme" {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write(blocked)

				return
			}

			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write(forbidden)
		}, nil)

	require.NoError(t, res.err)
	require.Equal(t, blocked, gunzipFile(t, filepath.Join(res.rawDir, "t_429_p0.json.gz")))
	require.Equal(t, forbidden, gunzipFile(t, filepath.Join(res.rawDir, "t_403_p0.json.gz")))
}

func TestSearchReportE2E_403IsPageError(t *testing.T) {
	res := runBatch(t, []string{taskLine("t_1", "roofer")}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("forbidden"))
	}, nil)

	require.NoError(t, res.err)
	require.EqualValues(t, 1, res.hits)
	require.Equal(t, "error", res.lines[0]["status"])
	require.Nil(t, res.lines[0]["block_reason"])
	require.Equal(t, []int{403}, intsOf(t, res.lines[0]["http_statuses"]))
}

func TestSearchReportE2E_AS13_KeySetThreeTasks(t *testing.T) {
	p0 := readFixture(t, "har_01_roofing-contractor_o0.json")

	res := runBatch(t, []string{
		`{"id":"t_ok","query":"fine","lat":30.2,"lng":-97.7,"zoom":14,"max_pages":1}`,
		`{"id":"t_empty","query":"nothing","lat":30.2,"lng":-97.7,"zoom":14,"max_pages":1}`,
		`{"id":"t_blocked","query":"blockme","lat":30.2,"lng":-97.7,"zoom":14,"max_pages":1}`,
	}, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("q") {
		case "fine":
			_, _ = w.Write(p0)
		case "nothing":
			_, _ = w.Write([]byte(")]}'\n[[null,null]]"))
		default:
			w.WriteHeader(http.StatusTooManyRequests)
		}
	}, nil)

	require.NoError(t, res.err)
	require.Len(t, res.lines, 3)

	status := map[string]string{}

	for _, line := range res.lines {
		keys := make([]string, 0, len(line))
		for k := range line {
			keys = append(keys, k)
		}

		require.ElementsMatch(t, contractKeys, keys)
		require.Equal(t, gmaps.ContractVersion, line["contract"])
		require.Equal(t, runner.EngineVersion(), line["engine_version"])

		id, _ := line["id"].(string)
		st, _ := line["status"].(string)
		status[id] = st
	}

	require.Equal(t, map[string]string{"t_ok": "ok", "t_empty": "empty", "t_blocked": "blocked"}, status)
}

func TestSearchReportE2E_NoReportFlagStillClassifies(t *testing.T) {
	res := runBatch(t, []string{taskLine("t_1", "roofer")}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}, func(cfg *runner.Config) { cfg.SearchReport = "" })

	require.NoError(t, res.err)
	require.EqualValues(t, 1, res.hits, "a block is requested once even without -search-report")
	require.NoFileExists(t, filepath.Join(res.dir, "searches.jsonl"))
}

func TestSearchReportE2E_FollowedRedirectIsNormal(t *testing.T) {
	p0 := readFixture(t, "har_01_roofing-contractor_o0.json")

	res := runBatch(t, []string{`{"id":"t_1","query":"roofer","lat":30.2,"lng":-97.7,"zoom":14,"max_pages":1}`},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/final" {
				_, _ = w.Write(p0)

				return
			}

			http.Redirect(w, r, "/final?"+r.URL.RawQuery, http.StatusFound) //nolint:gosec // loopback test server, the target is a constant path
		}, nil)

	require.NoError(t, res.err)
	require.Equal(t, "ok", res.lines[0]["status"], "the fetcher follows the redirect; the final response is classified")
	require.Equal(t, []int{200}, intsOf(t, res.lines[0]["http_statuses"]))
}
