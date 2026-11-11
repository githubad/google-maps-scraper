package filerunner_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gosom/google-maps-scraper/runner"
	"github.com/gosom/google-maps-scraper/runner/filerunner"
)

func jsonlCfg(t *testing.T, dir, tasks string) *runner.Config {
	t.Helper()
	t.Setenv("DISABLE_TELEMETRY", "1")

	input := filepath.Join(dir, "tasks.jsonl")
	require.NoError(t, os.WriteFile(input, []byte(tasks), 0o600))

	cfg := testConfig(input, filepath.Join(dir, "results.jsonl"))
	cfg.InputFormat = "jsonl"
	cfg.JSON = true

	return cfg
}

func TestJSONLReportFileOpenFailureIsExit4(t *testing.T) {
	var hits atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()

	dir := t.TempDir()
	cfg := jsonlCfg(t, dir, taskLine("t_1", "roofer")+"\n")
	cfg.MapsBaseURL = srv.URL
	cfg.SearchReport = filepath.Join(dir, "missing-dir", "searches.jsonl")

	_, err := filerunner.New(cfg)

	var ec *runner.ExitCodeError

	require.ErrorAs(t, err, &ec)
	require.Equal(t, runner.ExitWriteFailed, ec.Code)
	require.Zero(t, hits.Load(), "no request is made when the report file cannot be opened")
}

func TestJSONLResultsFileOpenFailureIsExit4(t *testing.T) {
	dir := t.TempDir()
	cfg := jsonlCfg(t, dir, taskLine("t_1", "roofer")+"\n")
	cfg.ResultsFile = filepath.Join(dir, "missing-dir", "results.jsonl")

	_, err := filerunner.New(cfg)

	var ec *runner.ExitCodeError

	require.ErrorAs(t, err, &ec)
	require.Equal(t, runner.ExitWriteFailed, ec.Code)
}

func TestJSONLInvalidFileCreatesNoReportFile(t *testing.T) {
	dir := t.TempDir()
	cfg := jsonlCfg(t, dir, `{"id":"t_1","query":"roofer","lat":95,"lng":-97.7,"zoom":14}`+"\n")
	cfg.SearchReport = filepath.Join(dir, "searches.jsonl")

	_, err := filerunner.New(cfg)

	var inputErr *runner.InputError

	require.ErrorAs(t, err, &inputErr)
	require.NoFileExists(t, cfg.SearchReport)
	require.NoFileExists(t, cfg.ResultsFile)
}

func TestJSONLZeroTasksWithReportFile(t *testing.T) {
	dir := t.TempDir()
	cfg := jsonlCfg(t, dir, "\n\n")
	cfg.SearchReport = filepath.Join(dir, "searches.jsonl")

	r, err := filerunner.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close(t.Context()) })

	require.NoError(t, r.Run(t.Context()))

	info, err := os.Stat(cfg.SearchReport)
	require.NoError(t, err, "the report file exists")
	require.Zero(t, info.Size(), "and is empty")
}
