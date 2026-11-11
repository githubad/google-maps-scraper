package gmaps

import (
	"context"
	"sync"
	"time"

	"github.com/gosom/scrapemate"
)

// DefaultAbortGrace is how long an early abort waits for requests that are
// already in flight before it cancels the run anyway.
const DefaultAbortGrace = 30 * time.Second

// abortPoll is how often the controller checks whether anything is still in flight.
const abortPoll = 10 * time.Millisecond

// AbortController implements the early abort of -max-blocked: once triggered,
// the gate in front of the job provider stops handing out jobs, and the run is
// cancelled when no job is in flight any more or after the grace period,
// whichever comes first.
type AbortController struct {
	mu       sync.Mutex
	aborted  chan struct{}
	once     sync.Once
	inFlight int
	grace    time.Duration
	cancel   func()
}

// NewAbortController returns a controller with the given grace period (zero or
// negative means DefaultAbortGrace).
func NewAbortController(grace time.Duration) *AbortController {
	if grace <= 0 {
		grace = DefaultAbortGrace
	}

	return &AbortController{aborted: make(chan struct{}), grace: grace}
}

// SetCancel sets the function that ends the run.
func (a *AbortController) SetCancel(fn func()) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.cancel = fn
}

// Trigger starts the abort; further calls do nothing.
func (a *AbortController) Trigger() {
	a.once.Do(func() {
		close(a.aborted)

		go a.cancelWhenIdle()
	})
}

// Aborted reports whether the abort was triggered. A nil controller never is.
func (a *AbortController) Aborted() bool {
	if a == nil {
		return false
	}

	select {
	case <-a.aborted:
		return true
	default:
		return false
	}
}

// JobFinished tells the controller that a job it handed out has been processed.
// A nil controller ignores it, so jobs without a gate can call it.
func (a *AbortController) JobFinished() {
	if a == nil {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.inFlight > 0 {
		a.inFlight--
	}
}

func (a *AbortController) begin() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.inFlight++
}

func (a *AbortController) inFlightCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.inFlight
}

func (a *AbortController) cancelWhenIdle() {
	deadline := time.NewTimer(a.grace)
	defer deadline.Stop()

	tick := time.NewTicker(abortPoll)
	defer tick.Stop()

	for a.inFlightCount() > 0 {
		select {
		case <-deadline.C:
			a.doCancel()

			return
		case <-tick.C:
		}
	}

	a.doCancel()
}

func (a *AbortController) doCancel() {
	a.mu.Lock()
	fn := a.cancel
	a.mu.Unlock()

	if fn != nil {
		fn()
	}
}

// gatedProvider is a job provider (an in-memory one, with scrapemate's three
// priorities) that stops handing out jobs once the abort is triggered. It holds
// at most one job per worker, like the stock provider, so a slow request never
// keeps queued tasks from an idle worker.
type gatedProvider struct {
	ctl *AbortController
	p0  chan scrapemate.IJob // high: next pages of a running task
	p1  chan scrapemate.IJob // medium: new tasks
	p2  chan scrapemate.IJob // low
}

// NewAbortGate returns the job provider of a run with -max-blocked. After ctl is
// triggered no job is handed to a worker any more and new jobs are dropped; tasks
// that never start are reported by the tracker's FinalizeAll.
func NewAbortGate(ctl *AbortController) scrapemate.JobProvider {
	return &gatedProvider{
		ctl: ctl,
		p0:  make(chan scrapemate.IJob),
		p1:  make(chan scrapemate.IJob),
		p2:  make(chan scrapemate.IJob),
	}
}

// Push queues the job, or drops it once the abort has been triggered.
func (g *gatedProvider) Push(ctx context.Context, job scrapemate.IJob) error {
	if g.ctl.Aborted() {
		return nil
	}

	// like the stock provider, a goroutine waits for a worker to take the job
	go func() {
		ch := g.p0

		switch job.GetPriority() {
		case scrapemate.PriorityMedium:
			ch = g.p1
		case scrapemate.PriorityLow:
			ch = g.p2
		case scrapemate.PriorityHigh:
		}

		select {
		case ch <- job:
		case <-ctx.Done():
		}
	}()

	return nil
}

// next returns the next job, strictly by priority; ok is false when ctx is done.
func (g *gatedProvider) next(ctx context.Context) (job scrapemate.IJob, ok bool) {
	select {
	case job = <-g.p0:
		return job, true
	default:
	}

	select {
	case job = <-g.p0:
		return job, true
	case job = <-g.p1:
		return job, true
	default:
	}

	select {
	case <-ctx.Done():
		return nil, false
	case job = <-g.p0:
		return job, true
	case job = <-g.p1:
		return job, true
	case job = <-g.p2:
		return job, true
	}
}

// Jobs returns the channel one worker reads from. A job the gate holds when the
// abort is triggered is dropped instead of delivered.
func (g *gatedProvider) Jobs(ctx context.Context) (jobs <-chan scrapemate.IJob, errs <-chan error) {
	out := make(chan scrapemate.IJob)
	errc := make(chan error, 1)

	go func() {
		for {
			job, ok := g.next(ctx)
			if !ok {
				errc <- ctx.Err()

				return
			}

			if g.ctl.Aborted() {
				continue // drop: the task is reported by FinalizeAll
			}

			g.ctl.begin()

			select {
			case out <- job:
			case <-g.ctl.aborted:
				g.ctl.JobFinished()
			case <-ctx.Done():
				g.ctl.JobFinished()

				errc <- ctx.Err()

				return
			}
		}
	}()

	return out, errc
}
