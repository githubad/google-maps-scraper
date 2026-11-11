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

type gatedProvider struct {
	inner scrapemate.JobProvider
	ctl   *AbortController
}

// NewAbortGate wraps a job provider so that, after ctl is triggered, no job is
// handed to a worker any more and new jobs are dropped. Tasks that never start
// are reported by the tracker's FinalizeAll.
func NewAbortGate(inner scrapemate.JobProvider, ctl *AbortController) scrapemate.JobProvider {
	return &gatedProvider{inner: inner, ctl: ctl}
}

// Push drops the job once the abort has been triggered.
func (g *gatedProvider) Push(ctx context.Context, job scrapemate.IJob) error {
	if g.ctl.Aborted() {
		return nil
	}

	return g.inner.Push(ctx, job)
}

// Jobs returns a channel fed from the inner provider. A job the gate holds
// when the abort is triggered is dropped instead of delivered.
func (g *gatedProvider) Jobs(ctx context.Context) (jobs <-chan scrapemate.IJob, errs <-chan error) {
	in, errc := g.inner.Jobs(ctx)
	out := make(chan scrapemate.IJob)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-g.ctl.aborted:
				// drain and drop whatever the inner provider still holds
				for {
					select {
					case <-in:
					case <-ctx.Done():
						return
					}
				}
			case job := <-in:
				if g.ctl.Aborted() {
					continue
				}

				g.ctl.begin()

				select {
				case out <- job:
				case <-g.ctl.aborted:
					g.ctl.JobFinished()
				case <-ctx.Done():
					g.ctl.JobFinished()

					return
				}
			}
		}
	}()

	return out, errc
}
