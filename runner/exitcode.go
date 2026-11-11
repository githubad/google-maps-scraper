package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/gosom/scrapemate"

	"github.com/gosom/google-maps-scraper/gmaps"
)

// Process exit codes of a jsonl run (docs/contracts.md §1.3). When more than one
// condition applies the order of precedence is 2, 1, 4, 3, 0.
const (
	ExitFatal        = 1
	ExitInvalidInput = exitInvalidInput
	ExitAborted      = 3
	ExitWriteFailed  = 4
)

// ExitCodeError makes the process exit with Code. Err, when set, is printed
// (masked, on one line) before exiting.
type ExitCodeError struct {
	Code int
	Err  error
}

func (e *ExitCodeError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit code %d", e.Code)
	}

	return gmaps.SanitizeError(e.Err.Error())
}

func (e *ExitCodeError) Unwrap() error { return e.Err }

// IsCleanEnd reports whether err is how a run that is over normally ends: nil,
// the context cancelled by the exit monitor, by SIGINT/SIGTERM through main, or
// by scrapemate's own signal handler, or the inactivity timeout.
func IsCleanEnd(err error) bool {
	return err == nil ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, scrapemate.ErrorExitSignal) ||
		errors.Is(err, scrapemate.ErrInactivityTimeout)
}

// RunFatal returns err when it is an unexpected fatal error of the run, and nil
// when the run ended cleanly or the error is explained by a write failure of the
// results or the report (that is then exit code 4, not a separate fatal error).
func RunFatal(err error, writeFailed bool) error {
	if IsCleanEnd(err) || writeFailed {
		return nil
	}

	return err
}

// ResolveExit applies the precedence of docs/contracts.md §1.3 for a run that
// started: 1 (fatal error), 4 (could not write results or report), 3 (aborted
// by -max-blocked), 0. Exit code 2, invalid input, is decided before any run.
func ResolveExit(fatal error, writeFailed, aborted bool) int {
	switch {
	case fatal != nil:
		return ExitFatal
	case writeFailed:
		return ExitWriteFailed
	case aborted:
		return ExitAborted
	default:
		return 0
	}
}

// SummaryLine is the one-line stderr summary written when a jsonl run ends.
func SummaryLine(c gmaps.Summary, code int) string {
	return fmt.Sprintf("tasks=%d ok=%d empty=%d partial=%d blocked=%d parse_error=%d error=%d skipped=%d exit=%d",
		c.Tasks, c.OK, c.Empty, c.Partial, c.Blocked, c.ParseError, c.Error, c.Skipped, code)
}
