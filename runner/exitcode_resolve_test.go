package runner_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/gosom/scrapemate"
	"github.com/stretchr/testify/require"

	"github.com/gosom/google-maps-scraper/gmaps"
	"github.com/gosom/google-maps-scraper/runner"
)

// AS-23: precedence 1 (fatal) > 4 (write failure) > 3 (abort) > 0.
func TestResolveExit(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")

	tests := []struct {
		name        string
		fatal       error
		writeFailed bool
		aborted     bool
		want        int
	}{
		{"nothing wrong", nil, false, false, 0},
		{"abort", nil, false, true, 3},
		{"write failure", nil, true, false, 4},
		{"write failure beats abort", nil, true, true, 4},
		{"fatal beats abort", boom, false, true, 1},
		{"fatal beats write failure and abort", boom, true, true, 1},
		{"fatal alone", boom, false, false, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, runner.ResolveExit(tc.fatal, tc.writeFailed, tc.aborted))
		})
	}
}

func TestRunFatal(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")

	require.NoError(t, runner.RunFatal(nil, false))
	require.NoError(t, runner.RunFatal(context.Canceled, false), "how a finished run is reported")
	require.NoError(t, runner.RunFatal(scrapemate.ErrorExitSignal, false), "SIGINT/SIGTERM through scrapemate's handler")
	require.NoError(t, runner.RunFatal(fmt.Errorf("%w: at 12:00", scrapemate.ErrInactivityTimeout), false))
	require.NoError(t, runner.RunFatal(fmt.Errorf("wrapped: %w", context.Canceled), false))
	require.ErrorIs(t, runner.RunFatal(boom, false), boom)
	require.NoError(t, runner.RunFatal(boom, true), "an error explained by a write failure is not a separate fatal error")
}

func TestIsCleanEnd(t *testing.T) {
	t.Parallel()

	require.True(t, runner.IsCleanEnd(nil))
	require.True(t, runner.IsCleanEnd(context.Canceled))
	require.True(t, runner.IsCleanEnd(scrapemate.ErrorExitSignal))
	require.False(t, runner.IsCleanEnd(errors.New("boom")))
	require.False(t, runner.IsCleanEnd(context.DeadlineExceeded))
}

func TestSummaryLine(t *testing.T) {
	t.Parallel()

	got := runner.SummaryLine(gmaps.Summary{Tasks: 10, Blocked: 2, Skipped: 8}, 3)
	require.Equal(t, "tasks=10 ok=0 empty=0 partial=0 blocked=2 parse_error=0 error=0 skipped=8 exit=3", got)

	got = runner.SummaryLine(gmaps.Summary{Tasks: 7, OK: 3, Empty: 1, Partial: 1, ParseError: 1, Error: 1}, 0)
	require.Equal(t, "tasks=7 ok=3 empty=1 partial=1 blocked=0 parse_error=1 error=1 skipped=0 exit=0", got)
}
