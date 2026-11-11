package gmaps

import (
	"context"
	"math/rand/v2"
	"time"
)

// sleepJitter pauses for a random duration, uniform in [lo, hi], and returns
// early with the context's error when ctx is cancelled. It never sleeps less
// than lo; a hi below lo is treated as lo (the flags are validated before a
// run, so this only protects the helper). A zero range returns at once.
func sleepJitter(ctx context.Context, lo, hi time.Duration) error {
	hi = max(hi, lo)
	if hi <= 0 {
		return nil
	}

	d := lo
	if hi > lo {
		d += time.Duration(rand.Int64N(int64(hi-lo) + 1)) //nolint:gosec // politeness jitter, not security
	}

	if d <= 0 {
		return nil
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
