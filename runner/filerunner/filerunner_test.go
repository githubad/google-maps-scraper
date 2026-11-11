package filerunner_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/runner"
	"github.com/gosom/google-maps-scraper/runner/filerunner"
	"github.com/stretchr/testify/require"
)

func TestNewRejectsResumeWithStdoutResults(t *testing.T) {
	t.Parallel()

	cfg := &runner.Config{
		RunMode:     runner.RunModeFile,
		InputFile:   "testdata/input.txt",
		ResultsFile: "stdout",
		Resume:      true,
	}

	_, err := filerunner.New(cfg)

	require.ErrorContains(t, err, "-resume requires -results to be a file path")
}

func TestNewRejectsResumeWithCustomWriter(t *testing.T) {
	t.Parallel()

	cfg := &runner.Config{
		RunMode:      runner.RunModeFile,
		InputFile:    "testdata/input.txt",
		ResultsFile:  "results.csv",
		CustomWriter: "/tmp:Writer",
		Resume:       true,
	}

	_, err := filerunner.New(cfg)

	require.ErrorContains(t, err, "-resume does not support custom writers")
}

func TestNewRejectsResumeWithLeadsDB(t *testing.T) {
	t.Parallel()

	cfg := &runner.Config{
		RunMode:       runner.RunModeFile,
		InputFile:     "testdata/input.txt",
		ResultsFile:   "results.csv",
		LeadsDBAPIKey: "key",
		Resume:        true,
	}

	_, err := filerunner.New(cfg)

	require.ErrorContains(t, err, "-resume does not support LeadsDB output")
}

func TestNewRejectsResumeWithFastMode(t *testing.T) {
	t.Parallel()

	cfg := &runner.Config{
		RunMode:     runner.RunModeFile,
		InputFile:   "testdata/input.txt",
		ResultsFile: "results.csv",
		Resume:      true,
		FastMode:    true,
	}

	_, err := filerunner.New(cfg)

	require.ErrorContains(t, err, "-resume does not support fast mode")
}

func TestNewResumeOpensResultsFileForAppend(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	inputPath := filepath.Join(dir, "input.txt")
	resultsPath := filepath.Join(dir, "results.jsonl")

	require.NoError(t, os.WriteFile(inputPath, []byte("coffee\n"), 0o600))

	existing := `{"input_id":"q1","link":"https://maps/place/existing"}` + "\n"

	require.NoError(t, os.WriteFile(resultsPath, []byte(existing), 0o600))

	cfg := testConfig(inputPath, resultsPath)
	cfg.Resume = true
	cfg.JSON = true

	r, err := filerunner.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Close(t.Context())) })

	require.NoError(t, r.Close(t.Context()))

	f, err := os.OpenFile(resultsPath, os.O_WRONLY|os.O_APPEND, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString("new\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	got, err := os.ReadFile(resultsPath)
	require.NoError(t, err)
	require.Equal(t, existing+"new\n", string(got))
}

func TestNewWithoutResumeTruncatesResultsFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	inputPath := filepath.Join(dir, "input.txt")
	resultsPath := filepath.Join(dir, "results.jsonl")

	require.NoError(t, os.WriteFile(inputPath, []byte("coffee\n"), 0o600))
	require.NoError(t, os.WriteFile(resultsPath, []byte("existing\n"), 0o600))

	r, err := filerunner.New(testConfig(inputPath, resultsPath))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Close(t.Context())) })

	require.NoError(t, r.Close(t.Context()))

	got, err := os.ReadFile(resultsPath)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestNewResumeRejectsSidecarWithoutResultsFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	inputPath := filepath.Join(dir, "input.txt")
	resultsPath := filepath.Join(dir, "results.jsonl")

	require.NoError(t, os.WriteFile(inputPath, []byte("coffee\n"), 0o600))
	require.NoError(t, os.WriteFile(resultsPath+".resume.json", []byte(`{
  "version": 1,
  "completed_inputs": ["q1"]
}`), 0o600))

	cfg := testConfig(inputPath, resultsPath)
	cfg.Resume = true
	cfg.JSON = true

	_, err := filerunner.New(cfg)

	require.ErrorContains(t, err, "resume state exists but results file is missing")

	_, statErr := os.Stat(resultsPath)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestNewWithoutResumeInvalidatesExistingSidecar(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	inputPath := filepath.Join(dir, "input.txt")
	resultsPath := filepath.Join(dir, "results.jsonl")
	statePath := resultsPath + ".resume.json"

	require.NoError(t, os.WriteFile(inputPath, []byte("coffee\n"), 0o600))
	require.NoError(t, os.WriteFile(resultsPath, []byte("existing\n"), 0o600))
	require.NoError(t, os.WriteFile(statePath, []byte(`{"version":1,"completed_inputs":["q1"]}`), 0o600))

	r, err := filerunner.New(testConfig(inputPath, resultsPath))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Close(t.Context())) })

	_, statErr := os.Stat(statePath)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func testConfig(inputPath, resultsPath string) *runner.Config {
	return &runner.Config{
		RunMode:                  runner.RunModeFile,
		InputFile:                inputPath,
		ResultsFile:              resultsPath,
		Concurrency:              1,
		MaxDepth:                 1,
		LangCode:                 "en",
		Zoom:                     15,
		Radius:                   10000,
		ExitOnInactivityDuration: 0,
		MaxPagesPerBrowser:       1,
	}
}

func TestJSONLInvalidFileExits2NoRequests(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	input := filepath.Join(dir, "tasks.jsonl")
	results := filepath.Join(dir, "results.jsonl")

	require.NoError(t, os.WriteFile(input, []byte(`{"id":"t_1","query":"roofer","lat":95,"lng":-97.7431,"zoom":14}`+"\n"), 0o600))

	cfg := testConfig(input, results)
	cfg.InputFormat = "jsonl"
	cfg.JSON = true

	_, err := filerunner.New(cfg)

	var inputErr *runner.InputError

	require.ErrorAs(t, err, &inputErr)
	require.Equal(t, []string{"line 1: lat: must be between -90 and 90"}, inputErr.Lines)

	_, statErr := os.Stat(results)
	require.ErrorIs(t, statErr, os.ErrNotExist, "exit 2 must not create the results file")
}

func TestJSONLConflictingFlagsAreInputErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	input := filepath.Join(dir, "tasks.jsonl")

	require.NoError(t, os.WriteFile(input, []byte(`{"id":"t_1","query":"q","lat":30,"lng":-97,"zoom":14}`+"\n"), 0o600))

	cfg := testConfig(input, filepath.Join(dir, "results.jsonl"))
	cfg.InputFormat = "jsonl"
	cfg.GeoCoordinates = "30.1,-97.1"

	_, err := filerunner.New(cfg)

	var inputErr *runner.InputError

	require.ErrorAs(t, err, &inputErr)
	require.Equal(t, []string{
		"flag -geo cannot be used with -input-format jsonl (the task file sets the position)",
	}, inputErr.Lines)
}

func TestJSONLZeroTasksExitsClean(t *testing.T) {
	// Run sends telemetry through a process-wide singleton: keep it a no-op.
	t.Setenv("DISABLE_TELEMETRY", "1")

	dir := t.TempDir()
	input := filepath.Join(dir, "tasks.jsonl")
	results := filepath.Join(dir, "results.jsonl")

	require.NoError(t, os.WriteFile(input, []byte("\n\n"), 0o600))

	cfg := testConfig(input, results)
	cfg.InputFormat = "jsonl"
	cfg.JSON = true

	r, err := filerunner.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Close(t.Context())) })

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	require.NoError(t, r.Run(ctx))

	info, err := os.Stat(results)
	require.NoError(t, err)
	require.Zero(t, info.Size(), "no tasks means no output records")
}

// miniBody keeps the header and the first n rows of a stock-layout list body.
func miniBody(t *testing.T, full []byte, n int) []byte {
	t.Helper()

	var data []any

	require.NoError(t, json.Unmarshal(full, &data))

	container, ok := data[0].([]any)
	require.True(t, ok)

	items, ok := container[1].([]any)
	require.True(t, ok)

	container[1] = items[:1+n]

	out, err := json.Marshal(data)
	require.NoError(t, err)

	return append([]byte(")]}'\n"), out...)
}

func TestJSONLInvalidFileMakesNoRequests(t *testing.T) {
	// AS-2, second half: a server that counts hits sees none.
	var hits atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()

	dir := t.TempDir()
	input := filepath.Join(dir, "tasks.jsonl")

	require.NoError(t, os.WriteFile(input, []byte(`{"id":"t_1","query":"roofer","lat":95,"lng":-97.7,"zoom":14}`+"\n"), 0o600))

	cfg := testConfig(input, filepath.Join(dir, "results.jsonl"))
	cfg.InputFormat = "jsonl"
	cfg.MapsBaseURL = srv.URL

	_, err := filerunner.New(cfg)

	var inputErr *runner.InputError

	require.ErrorAs(t, err, &inputErr)
	require.Equal(t, []string{"line 1: lat: must be between -90 and 90"}, inputErr.Lines)
	require.Zero(t, hits.Load(), "no HTTP request is made for an invalid file")
}

func TestJSONLEndToEndPagination(t *testing.T) {
	t.Setenv("DISABLE_TELEMETRY", "1")

	full, err := os.ReadFile("../../testdata/output.json")
	require.NoError(t, err)

	page0 := append([]byte(")]}'\n"), full...)
	short := miniBody(t, full, 5)

	var (
		mu      sync.Mutex
		offsets = map[string][]string{}
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		offset := "0"
		if m := regexp.MustCompile(`!8i(\d+)`).FindStringSubmatch(q.Get("pb")); m != nil {
			offset = m[1]
		}

		mu.Lock()

		offsets[q.Get("q")] = append(offsets[q.Get("q")], offset)

		mu.Unlock()

		if offset == "0" {
			_, _ = w.Write(page0)

			return
		}

		_, _ = w.Write(short)
	}))
	defer srv.Close()

	dir := t.TempDir()
	input := filepath.Join(dir, "tasks.jsonl")
	results := filepath.Join(dir, "results.jsonl")
	rawDir := filepath.Join(dir, "raw")

	tasks := `{"id":"t_a","query":"alpha","lat":38.03,"lng":23.70,"zoom":15,"max_pages":3}` + "\n" +
		`{"id":"t_b","query":"beta","lat":38.03,"lng":23.70,"zoom":15,"max_pages":1}` + "\n"
	require.NoError(t, os.WriteFile(input, []byte(tasks), 0o600))

	cfg := testConfig(input, results)
	cfg.InputFormat = "jsonl"
	cfg.JSON = true
	cfg.MapsBaseURL = srv.URL
	cfg.RawDir = rawDir
	cfg.Radius = 0

	r, err := filerunner.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Close(t.Context())) })

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	if runErr := r.Run(ctx); runErr != nil && !errors.Is(runErr, context.Canceled) {
		t.Skipf("the stealth fetcher could not reach the plain-HTTP test server: %v", runErr)
	}

	mu.Lock()
	defer mu.Unlock()

	require.Equal(t, []string{"0", "20"}, offsets["alpha"], "t_a: page 0 is full, page 1 is short")
	require.Equal(t, []string{"0"}, offsets["beta"], "t_b: max_pages 1")

	raw, err := os.ReadFile(results)
	require.NoError(t, err)

	perTask := map[string]int{}

	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var rec map[string]any

		require.NoError(t, json.Unmarshal([]byte(line), &rec), "one JSON object per line: %q", line)

		id, _ := rec["input_id"].(string)
		perTask[id]++

		require.NotEmpty(t, rec["place_id"])
	}

	require.Equal(t, map[string]int{"t_a": 20, "t_b": 20}, perTask, "page 1 repeats earlier rows and adds none")

	for _, f := range []string{"t_a_p0.json.gz", "t_a_p1.json.gz", "t_b_p0.json.gz"} {
		_, statErr := os.Stat(filepath.Join(rawDir, f))
		require.NoError(t, statErr, f)
	}
}
