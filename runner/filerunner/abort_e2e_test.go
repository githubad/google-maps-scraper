package filerunner_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gosom/google-maps-scraper/runner"
)

func manyTasks(n int) []string {
	lines := make([]string, 0, n)

	for i := 1; i <= n; i++ {
		lines = append(lines, taskLine(fmt.Sprintf("t_%02d", i), fmt.Sprintf("query%02d", i)))
	}

	return lines
}

func always429(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte("<html>unusual traffic</html>"))
}

func statusCounts(lines []map[string]any) map[string]int {
	counts := map[string]int{}

	for _, l := range lines {
		st, _ := l["status"].(string)
		counts[st]++
	}

	return counts
}

func requireExitCode(t *testing.T, err error, code int) {
	t.Helper()

	var ec *runner.ExitCodeError

	require.ErrorAs(t, err, &ec)
	require.Equal(t, code, ec.Code)
}

func TestMaxBlockedE2E_AS8(t *testing.T) {
	res := runBatch(t, manyTasks(10), always429, func(cfg *runner.Config) { cfg.MaxBlocked = 2 })

	requireExitCode(t, res.err, 3)
	require.Len(t, res.lines, 10)
	require.Equal(t, map[string]int{"blocked": 2, "skipped": 8}, statusCounts(res.lines))
	require.EqualValues(t, 2, res.hits, "no request after the second block")

	ids := map[string]bool{}

	for _, l := range res.lines {
		id, _ := l["id"].(string)
		ids[id] = true

		if l["status"] == "skipped" {
			require.Equal(t, "aborted: max-blocked", l["error"])
		}
	}

	require.Len(t, ids, 10, "every task exactly one line")
}

func TestMaxBlockedE2E_Concurrent(t *testing.T) {
	res := runBatch(t, manyTasks(20), func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(30 * time.Millisecond):
		case <-r.Context().Done():
		}

		always429(w, r)
	}, func(cfg *runner.Config) {
		cfg.MaxBlocked = 2
		cfg.Concurrency = 4
	})

	requireExitCode(t, res.err, 3)
	require.Len(t, res.lines, 20)

	counts := statusCounts(res.lines)
	require.GreaterOrEqual(t, counts["blocked"], 2)
	require.LessOrEqual(t, res.hits, int64(2+4), "the threshold plus at most the requests already in flight")
	require.Equal(t, 20, counts["blocked"]+counts["skipped"]+counts["error"])

	ids := map[string]bool{}

	for _, l := range res.lines {
		id, _ := l["id"].(string)
		ids[id] = true

		if l["status"] != "blocked" {
			require.Equal(t, "aborted: max-blocked", l["error"], "a task that did not finish normally carries the reason")
		}
	}

	require.Len(t, ids, 20)
}

func TestMaxBlockedLargerThanTasksNeverTriggers(t *testing.T) {
	res := runBatch(t, manyTasks(3), always429, func(cfg *runner.Config) { cfg.MaxBlocked = 10 })

	require.NoError(t, res.err, "blocked tasks alone do not change the exit code")
	require.Equal(t, map[string]int{"blocked": 3}, statusCounts(res.lines))
}

func TestMaxBlockedPartialDoesNotCount(t *testing.T) {
	p0 := readFixture(t, "har_01_roofing-contractor_o0.json")

	res := runBatch(t, manyTasks(3), func(w http.ResponseWriter, r *http.Request) {
		if pageOffset(r) == 0 {
			_, _ = w.Write(p0)

			return
		}

		always429(w, r)
	}, func(cfg *runner.Config) { cfg.MaxBlocked = 1 })

	require.NoError(t, res.err)
	require.Equal(t, map[string]int{"partial": 3}, statusCounts(res.lines), "a block on page 1 is partial and never counts")
}

func TestMaxBlockedOffByDefault(t *testing.T) {
	res := runBatch(t, manyTasks(3), always429, nil)

	require.NoError(t, res.err)
	require.EqualValues(t, 3, res.hits)
	require.Equal(t, map[string]int{"blocked": 3}, statusCounts(res.lines))
}

// A slow request in flight at the abort does not hold the batch for ever: the
// run is cancelled after the grace period (shortened here from 30 s).
func TestMaxBlockedStopsWithinGrace(t *testing.T) {
	start := time.Now()

	res := runBatch(t, []string{taskLine("t_a", "block"), taskLine("t_b", "slow"), taskLine("t_c", "block")},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("q") == "slow" {
				select {
				case <-time.After(20 * time.Second):
				case <-r.Context().Done():
				}

				return
			}

			always429(w, r)
		}, func(cfg *runner.Config) {
			cfg.MaxBlocked = 1
			cfg.Concurrency = 2
			cfg.AbortGrace = 300 * time.Millisecond
		})

	requireExitCode(t, res.err, 3)
	require.Less(t, time.Since(start), 8*time.Second, "cancelled after the grace, not after the slow request")
	require.Len(t, res.lines, 3)

	for _, l := range res.lines {
		if l["id"] == "t_b" {
			require.Contains(t, []string{"error", "skipped"}, l["status"], "the slow task was cut off before any page succeeded")
			require.Equal(t, "aborted: max-blocked", l["error"])
		}
	}
}
