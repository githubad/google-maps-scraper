package gmaps

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSleepJitterWithinRange(t *testing.T) {
	t.Parallel()

	const (
		lo = 10 * time.Millisecond
		hi = 30 * time.Millisecond
	)

	seen := map[time.Duration]bool{}

	for range 30 {
		start := time.Now()

		require.NoError(t, sleepJitter(t.Context(), lo, hi))

		elapsed := time.Since(start)
		require.GreaterOrEqual(t, elapsed, lo)
		require.Less(t, elapsed, hi+80*time.Millisecond, "generous slack for a loaded machine")

		seen[elapsed.Round(5*time.Millisecond)] = true
	}

	require.Greater(t, len(seen), 1, "the delay is random, not constant")
}

func TestSleepJitterEqualBoundsSleepsExactly(t *testing.T) {
	t.Parallel()

	start := time.Now()

	require.NoError(t, sleepJitter(t.Context(), 40*time.Millisecond, 40*time.Millisecond))
	require.GreaterOrEqual(t, time.Since(start), 40*time.Millisecond)
}

func TestSleepJitterZeroReturnsAtOnce(t *testing.T) {
	t.Parallel()

	start := time.Now()

	require.NoError(t, sleepJitter(t.Context(), 0, 0))
	require.Less(t, time.Since(start), 20*time.Millisecond)
}

func TestSleepJitterCancelInterrupts(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := sleepJitter(ctx, 10*time.Second, 10*time.Second)

	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 300*time.Millisecond)
}

func TestSleepJitterMinAboveMaxUsesMin(t *testing.T) {
	t.Parallel()

	start := time.Now()

	require.NoError(t, sleepJitter(t.Context(), 30*time.Millisecond, 5*time.Millisecond))
	require.GreaterOrEqual(t, time.Since(start), 30*time.Millisecond, "validation rejects this; the helper still never sleeps less than min")
}
