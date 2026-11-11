package runner

import (
	"fmt"

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
