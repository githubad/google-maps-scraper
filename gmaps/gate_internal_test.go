package gmaps

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosom/scrapemate"
	"github.com/stretchr/testify/require"
)

func gateJob(id string) scrapemate.IJob {
	return &scrapemate.Job{ID: id, Priority: scrapemate.PriorityMedium}
}

// recv waits up to d for a job.
func recv(ch <-chan scrapemate.IJob, d time.Duration) (scrapemate.IJob, bool) {
	select {
	case j := <-ch:
		return j, true
	case <-time.After(d):
		return nil, false
	}
}

func newGate(t *testing.T, grace time.Duration) (scrapemate.JobProvider, *AbortController, *atomic.Int32) {
	t.Helper()

	var cancels atomic.Int32

	ctl := NewAbortController(grace)
	ctl.SetCancel(func() { cancels.Add(1) })

	return NewAbortGate(ctl), ctl, &cancels
}

func TestGatedProviderForwardsUntilTrigger(t *testing.T) {
	t.Parallel()

	gate, _, _ := newGate(t, time.Second)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	jobs, _ := gate.Jobs(ctx)

	for _, id := range []string{"a", "b", "c"} {
		require.NoError(t, gate.Push(ctx, gateJob(id)))
	}

	got := map[string]bool{}

	for range 3 {
		j, ok := recv(jobs, time.Second)
		require.True(t, ok)

		got[j.GetID()] = true
	}

	require.Equal(t, map[string]bool{"a": true, "b": true, "c": true}, got)
}

func TestGatedProviderDropsHeldJobsAfterTrigger(t *testing.T) {
	t.Parallel()

	gate, ctl, _ := newGate(t, 50*time.Millisecond)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	jobs, _ := gate.Jobs(ctx) // one worker (-c 1)

	for _, id := range []string{"a", "b", "c", "d", "e"} {
		require.NoError(t, gate.Push(ctx, gateJob(id)))
	}

	_, ok := recv(jobs, time.Second)
	require.True(t, ok, "the first job reaches the worker")

	ctl.Trigger()

	_, ok = recv(jobs, 200*time.Millisecond)
	require.False(t, ok, "after the trigger no further job is delivered, even though four were waiting")
}

func TestGatedProviderPushAfterTriggerIsDropped(t *testing.T) {
	t.Parallel()

	gate, ctl, _ := newGate(t, 50*time.Millisecond)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	jobs, _ := gate.Jobs(ctx)

	ctl.Trigger()
	require.NoError(t, gate.Push(ctx, gateJob("late")), "dropping is not an error")

	_, ok := recv(jobs, 200*time.Millisecond)
	require.False(t, ok)
}

func TestAbortControllerCancelsWhenIdle(t *testing.T) {
	t.Parallel()

	_, ctl, cancels := newGate(t, 5*time.Second)

	ctl.Trigger()

	require.Eventually(t, func() bool { return cancels.Load() == 1 }, 100*time.Millisecond, time.Millisecond,
		"nothing in flight: cancel at once")
}

func TestAbortControllerWaitsForInFlight(t *testing.T) {
	t.Parallel()

	_, ctl, cancels := newGate(t, 5*time.Second)

	ctl.begin()
	ctl.Trigger()

	time.Sleep(100 * time.Millisecond)
	require.Zero(t, cancels.Load(), "one job is still in flight")

	ctl.JobFinished()

	require.Eventually(t, func() bool { return cancels.Load() == 1 }, time.Second, 5*time.Millisecond)
}

func TestAbortControllerGraceExpires(t *testing.T) {
	t.Parallel()

	_, ctl, cancels := newGate(t, 80*time.Millisecond)

	ctl.begin() // never finishes
	ctl.Trigger()

	require.Eventually(t, func() bool { return cancels.Load() == 1 }, time.Second, 5*time.Millisecond,
		"cancelled after the grace although a job is still in flight")
}

func TestAbortControllerTriggerOnce(t *testing.T) {
	t.Parallel()

	_, ctl, cancels := newGate(t, time.Second)

	for range 10 {
		ctl.Trigger()
	}

	require.Eventually(t, func() bool { return cancels.Load() >= 1 }, time.Second, time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	require.EqualValues(t, 1, cancels.Load())
	require.True(t, ctl.Aborted())
}

func TestInFlightCounting(t *testing.T) {
	t.Parallel()

	ctl := NewAbortController(time.Second)

	ctl.begin()
	ctl.begin()
	require.Equal(t, 2, ctl.inFlightCount())

	ctl.JobFinished()
	require.Equal(t, 1, ctl.inFlightCount())

	ctl.JobFinished()
	ctl.JobFinished() // never below zero
	require.Zero(t, ctl.inFlightCount())

	var nilCtl *AbortController

	nilCtl.JobFinished() // a job without a gate calls it harmlessly
	require.False(t, nilCtl.Aborted())
}

func TestGatedProviderCountsDeliveredJobs(t *testing.T) {
	t.Parallel()

	gate, ctl, _ := newGate(t, time.Second)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	jobs, _ := gate.Jobs(ctx)

	require.NoError(t, gate.Push(ctx, gateJob("a")))

	_, ok := recv(jobs, time.Second)
	require.True(t, ok)
	require.Equal(t, 1, ctl.inFlightCount(), "a delivered job is in flight until it finishes")
}

func TestNewAbortControllerDefaultGrace(t *testing.T) {
	t.Parallel()

	require.Equal(t, DefaultAbortGrace, NewAbortController(0).grace)
	require.Equal(t, DefaultAbortGrace, NewAbortController(-time.Second).grace)
	require.Equal(t, 5*time.Second, NewAbortController(5*time.Second).grace)
}

func TestSearchJobGateIsToldWhenProcessed(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	ctl := NewAbortController(time.Second)
	srv := statusServer(t, map[int]reply{0: {status: 429, body: []byte("x")}})
	job := trackedJob(t, srv.URL, 3, env, WithSearchJobGate(ctl))

	require.Same(t, ctl, job.Gate)

	ctl.begin() // the gate handed the job to a worker
	require.Equal(t, 1, ctl.inFlightCount())

	_, _, err := job.Process(context.Background(), fetchJob(t, job))
	require.NoError(t, err)
	require.Zero(t, ctl.inFlightCount(), "Process reports that the job is done")
}

// A slow job must not hold queued jobs back from an idle worker (the flaw of a
// gate that prefetches on top of the stock provider's own prefetch).
func TestGatedProviderDoesNotHoardJobsBehindABusyWorker(t *testing.T) {
	t.Parallel()

	gate, _, _ := newGate(t, time.Second)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	busy, _ := gate.Jobs(ctx) // worker 1
	idle, _ := gate.Jobs(ctx) // worker 2

	for _, id := range []string{"a", "b", "c"} {
		require.NoError(t, gate.Push(ctx, gateJob(id)))
	}

	// worker 1 takes a job and then stays busy (never reads again); worker 2 must
	// still receive a job, whichever of the three it is
	_, ok := recv(busy, time.Second)
	require.True(t, ok)

	_, ok = recv(idle, time.Second)
	require.True(t, ok, "the idle worker gets a job although another worker is busy")
}

func TestGatedProviderServesHigherPriorityFirst(t *testing.T) {
	t.Parallel()

	gate, _, _ := newGate(t, time.Second)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	for _, id := range []string{"seed1", "seed2", "seed3"} {
		require.NoError(t, gate.Push(ctx, gateJob(id)))
	}

	require.NoError(t, gate.Push(ctx, &scrapemate.Job{ID: "page", Priority: scrapemate.PriorityHigh}))

	time.Sleep(50 * time.Millisecond) // let the push goroutines park on their channels

	jobs, _ := gate.Jobs(ctx)

	first, ok := recv(jobs, time.Second)
	require.True(t, ok)
	require.Equal(t, "page", first.GetID(), "a next-page job outranks tasks that have not started")
}
