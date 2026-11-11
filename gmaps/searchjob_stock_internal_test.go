package gmaps

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// AS-14 at job level: a job built without the F003 tracker keeps upstream's
// response check and retry count, and Process behaves as in F002.
func TestSearchJobWithoutTrackerIsStock(t *testing.T) {
	t.Parallel()

	full := outputPage(t)
	short := trimRows(t, full)

	srv, hits := newBodyServer(t, func(offset int) []byte {
		if offset >= 20 {
			return short
		}

		return full
	})

	job := newPagedJob(srv.URL, 3)

	require.Nil(t, job.CheckResponse, "upstream's default 2xx check")
	require.Equal(t, 3, job.GetMaxRetries(), "upstream's retry count")
	require.Nil(t, job.Tracker)

	run := runTask(t, job)

	require.Equal(t, []int{0, 20}, hits.Offsets())
	require.Empty(t, run.Errors)
	require.Len(t, run.Entries, 20)
	require.Equal(t, sequence(20), ranks(run.Entries))
}

func TestSearchJobGetFullURLWithoutTrackerIsPlain(t *testing.T) {
	t.Parallel()

	job := newPagedJob("http://127.0.0.1:1/search", 3)

	require.NotPanics(t, func() { _ = job.GetFullURL() })
	require.Contains(t, job.GetFullURL(), "http://127.0.0.1:1/search?")
}
