package runner_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gosom/google-maps-scraper/runner"
)

func TestExitCodeError(t *testing.T) {
	t.Parallel()

	cause := errors.New("cannot create report: open http://user:secret@host/x: no such file\nsecond line")
	err := error(&runner.ExitCodeError{Code: runner.ExitWriteFailed, Err: cause})

	var ec *runner.ExitCodeError

	require.ErrorAs(t, err, &ec)
	require.Equal(t, 4, ec.Code)
	require.Equal(t, "cannot create report: open http://***@host/x: no such file second line", err.Error(), "masked and single line")
	require.ErrorIs(t, err, cause, "Unwrap returns the cause")

	bare := &runner.ExitCodeError{Code: runner.ExitAborted}
	require.Equal(t, "exit code 3", bare.Error())
}

func TestExitCodeConstants(t *testing.T) {
	t.Parallel()

	require.Equal(t, 1, runner.ExitFatal)
	require.Equal(t, 2, runner.ExitInvalidInput)
	require.Equal(t, 3, runner.ExitAborted)
	require.Equal(t, 4, runner.ExitWriteFailed)
}
