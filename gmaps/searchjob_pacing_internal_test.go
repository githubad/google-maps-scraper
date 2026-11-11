package gmaps

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/gosom/scrapemate"
	"github.com/stretchr/testify/require"
)

// processAsync runs Process in a goroutine and returns a channel closed when it returns.
func processAsync(job *SearchJob, resp *scrapemate.Response) <-chan struct{} {
	done := make(chan struct{})

	go func() {
		defer close(done)

		_, _, _ = job.Process(context.Background(), resp)
	}()

	return done
}

func TestSearchJobSleepsAfterPage(t *testing.T) {
	t.Parallel()

	const delay = 300 * time.Millisecond

	env := newTrackerEnv(t)
	exit := &recordingExiter{}
	short := trimRows(t, outputPage(t))
	srv := statusServer(t, map[int]reply{0: {status: 200, body: short}})

	job := trackedJob(t, srv.URL, 1, env, WithSearchJobDelay(delay, delay), WithSearchJobExitMonitor(exit))
	resp := fetchJob(t, job)

	start := time.Now()
	done := processAsync(job, resp)

	// the report line is written before the sleep, and the exit monitor is told after it
	require.Eventually(t, func() bool {
		info, err := os.Stat(env.path)

		return err == nil && info.Size() > 0
	}, delay, 5*time.Millisecond, "the line is written while the job is still sleeping")

	select {
	case <-done:
		t.Fatal("Process returned before the delay")
	default:
	}

	exit.mu.Lock()
	early := exit.seedsCompleted
	exit.mu.Unlock()
	require.Zero(t, early, "the exit monitor hears about the page only after the sleep")

	<-done
	require.GreaterOrEqual(t, time.Since(start), delay)
	require.Equal(t, 1, exit.seedsCompleted)
}

func TestSearchJobSleepsAfterBlockedPage(t *testing.T) {
	t.Parallel()

	const delay = 100 * time.Millisecond

	env := newTrackerEnv(t)
	srv := statusServer(t, map[int]reply{0: {status: 429, body: []byte("slow down")}})
	job := trackedJob(t, srv.URL, 3, env, WithSearchJobDelay(delay, delay))

	start := time.Now()
	_, _, err := job.Process(context.Background(), fetchJob(t, job))

	require.NoError(t, err)
	require.GreaterOrEqual(t, time.Since(start), delay, "a blocked page is paced too")
}

func TestSearchJobNoSleepOnNetworkFailure(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	job := trackedJob(t, "http://127.0.0.1:1/search", 3, env, WithSearchJobDelay(2*time.Second, 2*time.Second))

	start := time.Now()
	_, _, err := job.Process(context.Background(), &scrapemate.Response{Error: context.DeadlineExceeded})

	require.NoError(t, err)
	require.Less(t, time.Since(start), time.Second, "no response, nothing to be polite about")
}

func TestSearchJobNoDelayByDefault(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	srv := statusServer(t, map[int]reply{0: {status: 429, body: []byte("x")}})
	job := trackedJob(t, srv.URL, 3, env)

	require.Zero(t, job.DelayMin)
	require.Zero(t, job.DelayMax)

	_, _, err := job.Process(context.Background(), fetchJob(t, job))
	require.NoError(t, err)
}
