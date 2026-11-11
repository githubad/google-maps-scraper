package filerunner_test

import (
	"io"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gosom/google-maps-scraper/runner"
	"github.com/gosom/google-maps-scraper/runner/filerunner"
)

func slowHandler(delay time.Duration, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(delay):
			_, _ = w.Write(body)
		case <-r.Context().Done():
		}
	}
}

func requireOneLinePerTask(t *testing.T, lines []map[string]any, n int) {
	t.Helper()

	ids := map[string]bool{}

	for _, l := range lines {
		id, _ := l["id"].(string)
		require.False(t, ids[id], "task %s has two lines", id)

		ids[id] = true
	}

	require.Len(t, ids, n)
}

// AS-10, with the run context cancelled the way main's handler does.
func TestFinalizeOnCancel_AS10(t *testing.T) {
	p0 := readFixture(t, "har_01_roofing-contractor_o0.json")

	res := runBatchCancel(t, manyTasks(20), slowHandler(150*time.Millisecond, p0), func(cfg *runner.Config) {
		cfg.Concurrency = 2
	}, 500*time.Millisecond)

	require.NoError(t, res.err, "a cancelled run with a complete report is a clean end (exit 0)")
	requireOneLinePerTask(t, res.lines, 20)

	counts := statusCounts(res.lines)
	require.Positive(t, counts["skipped"], "tasks that never started are skipped")
	require.Positive(t, counts["partial"], "tasks cut off after their first page are partial, not lost")
	require.Equal(t, 20, counts["skipped"]+counts["partial"]+counts["ok"]+counts["error"])
}

// A run that stalls ends on -exit-on-inactivity; every task still gets a line.
func TestFinalizeOnInactivity(t *testing.T) {
	res := runBatch(t, manyTasks(3), slowHandler(20*time.Second, nil), func(cfg *runner.Config) {
		cfg.ExitOnInactivityDuration = 300 * time.Millisecond
	})

	require.NoError(t, res.err)
	requireOneLinePerTask(t, res.lines, 3)

	for _, l := range res.lines {
		require.Equal(t, "interrupted", l["error"])
		require.Contains(t, []string{"error", "skipped"}, l["status"])
	}
}

// The real SIGTERM goes through scrapemate's own handler (research E7): the run
// still ends cleanly with a complete report.
func TestFinalizeOnRealSIGTERM(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs POSIX signals")
	}

	// the test process must survive the signal whatever scrapemate does
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGTERM)

	defer signal.Stop(sigc)

	p0 := readFixture(t, "har_01_roofing-contractor_o0.json")

	var first atomic.Bool

	started := make(chan struct{})

	handler := func(w http.ResponseWriter, r *http.Request) {
		if first.CompareAndSwap(false, true) {
			close(started)
		}

		slowHandler(150*time.Millisecond, p0)(w, r)
	}

	go func() {
		<-started
		time.Sleep(100 * time.Millisecond)

		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}()

	res := runBatch(t, manyTasks(20), handler, func(cfg *runner.Config) { cfg.Concurrency = 2 })

	require.NoError(t, res.err, "exit code 0 after SIGTERM with a complete report")
	requireOneLinePerTask(t, res.lines, 20)
	require.Positive(t, statusCounts(res.lines)["skipped"])
}

func TestFinalizeAfterRunEndsNormally(t *testing.T) {
	p0 := readFixture(t, "har_01_roofing-contractor_o0.json")

	res := runBatch(t, manyTasks(3), func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(p0) }, nil)

	require.NoError(t, res.err)
	requireOneLinePerTask(t, res.lines, 3)
}

func requireDevFull(t *testing.T) {
	t.Helper()

	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("needs /dev/full (a file every write to which fails)")
	}
}

func TestResultsWriteFailureExit4(t *testing.T) {
	requireDevFull(t)

	p0 := readFixture(t, "har_01_roofing-contractor_o0.json")

	res := runBatch(t, manyTasks(2), func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(p0) },
		func(cfg *runner.Config) { cfg.ResultsFile = "/dev/full" })

	requireExitCode(t, res.err, 4)
	requireOneLinePerTask(t, res.lines, 2)
}

func TestReportWriteFailureExit4(t *testing.T) {
	requireDevFull(t)

	p0 := readFixture(t, "har_01_roofing-contractor_o0.json")

	res := runBatch(t, manyTasks(2), func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(p0) },
		func(cfg *runner.Config) { cfg.SearchReport = "/dev/full" })

	requireExitCode(t, res.err, 4)
}

// FR-010: the one-line summary on stderr, with the final exit code.
func TestSummaryLineOnStderr(t *testing.T) {
	old := os.Stderr

	r, w, err := os.Pipe()
	require.NoError(t, err)

	os.Stderr = w

	var out strings.Builder

	done := make(chan struct{})

	go func() {
		defer close(done)

		_, _ = io.Copy(&out, r)
	}()

	res := runBatch(t, manyTasks(10), always429, func(cfg *runner.Config) { cfg.MaxBlocked = 2 })

	os.Stderr = old

	require.NoError(t, w.Close())
	<-done

	requireExitCode(t, res.err, 3)
	require.Contains(t, out.String(), "tasks=10 ok=0 empty=0 partial=0 blocked=2 parse_error=0 error=0 skipped=8 exit=3")
}

// No summary when the run exits 4 while opening a file (the run never started).
func TestNoSummaryWhenOpenFails(t *testing.T) {
	old := os.Stderr

	r, w, err := os.Pipe()
	require.NoError(t, err)

	os.Stderr = w

	dir := t.TempDir()
	cfg := jsonlCfg(t, dir, taskLine("t_1", "roofer")+"\n")
	cfg.SearchReport = dir + "/missing/searches.jsonl"

	_, newErr := filerunner.New(cfg)

	os.Stderr = old

	require.NoError(t, w.Close())

	captured, _ := io.ReadAll(r)

	requireExitCode(t, newErr, 4)
	require.NotContains(t, string(captured), "tasks=")
}
