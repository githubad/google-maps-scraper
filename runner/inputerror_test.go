package runner_test

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gosom/google-maps-scraper/runner"
)

func TestInputErrorLines(t *testing.T) {
	t.Parallel()

	e := &runner.InputError{Lines: []string{"line 1: lat: must be between -90 and 90", "flag -geo cannot be used"}}
	require.Equal(t, "line 1: lat: must be between -90 and 90\nflag -geo cannot be used", e.Error())

	ve := runner.ValidationError{Line: 7, Field: "span_m", Reason: "only valid in list mode"}
	require.Equal(t, "line 7: span_m: only valid in list mode", ve.String())
}

func TestReportInputError(t *testing.T) {
	t.Parallel()

	t.Run("wrapped input error prints each line and returns 2", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer

		err := fmt.Errorf("starting: %w", &runner.InputError{Lines: []string{"a", "b"}})

		code, ok := runner.ReportInputError(&out, err)
		require.True(t, ok)
		require.Equal(t, 2, code)
		require.Equal(t, "a\nb\n", out.String())
	})

	t.Run("other errors are not handled", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer

		code, ok := runner.ReportInputError(&out, errors.New("boom"))
		require.False(t, ok)
		require.Equal(t, 0, code)
		require.Empty(t, out.String())

		code, ok = runner.ReportInputError(&out, nil)
		require.False(t, ok)
		require.Equal(t, 0, code)
	})
}
