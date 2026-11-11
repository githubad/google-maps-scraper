package filerunner_test

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gosom/google-maps-scraper/runner"
)

// threePages serves 20, 20 and 5 rows for offsets 0, 20 and 40.
func threePages(t *testing.T) http.HandlerFunc {
	t.Helper()

	p0 := readFixture(t, "har_01_roofing-contractor_o0.json")
	p1 := alterPage(t, readFixture(t, "har_02_roofing-contractor_o20.json"), "x")

	full, err := os.ReadFile("../../testdata/output.json")
	require.NoError(t, err)

	p2 := miniBody(t, full, 5)

	return func(w http.ResponseWriter, r *http.Request) {
		switch pageOffset(r) {
		case 0:
			_, _ = w.Write(p0)
		case 20:
			_, _ = w.Write(p1)
		default:
			_, _ = w.Write(p2)
		}
	}
}

func TestPacingE2E_AS9(t *testing.T) {
	handler := threePages(t)
	start := time.Now()

	res := runBatch(t, []string{taskLine("t_1", "roofer")}, handler, func(cfg *runner.Config) {
		cfg.DelayMin = 200 * time.Millisecond
		cfg.DelayMax = 200 * time.Millisecond
	})

	elapsed := time.Since(start)

	require.NoError(t, res.err)
	require.EqualValues(t, 3, res.hits)
	require.Equal(t, "ok", res.lines[0]["status"])
	require.GreaterOrEqual(t, elapsed, 600*time.Millisecond, "three post-page sleeps")
	require.Less(t, elapsed, 2*time.Second)
}

// Without the flags nothing is paused. The job-level tests check the zero
// defaults; no wall-clock bound here, because the race detector makes timing
// assertions flaky.
func TestPacingE2E_NoDelayFlagsStillCompletes(t *testing.T) {
	handler := threePages(t)

	res := runBatch(t, []string{taskLine("t_1", "roofer")}, handler, nil)

	require.NoError(t, res.err)
	require.EqualValues(t, 3, res.hits)
	require.Equal(t, "ok", res.lines[0]["status"])
}
