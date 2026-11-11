package gmaps

import (
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func blockTask(env trackerEnv, id string) {
	env.tr.Register(id, ModeList, intPtr(6))
	env.tr.PageDone(id, PageResult{Page: 0, Status: 429, Outcome: PageBlocked, Reason: ReasonHTTP429})
	env.tr.Finish(id)
}

func TestTrackerBlockedThreshold(t *testing.T) {
	t.Parallel()

	t.Run("fires once, on the Nth blocked finish", func(t *testing.T) {
		t.Parallel()

		env := newTrackerEnv(t)

		var fired atomic.Int32

		env.tr.SetBlockedThreshold(2, func() { fired.Add(1) })

		blockTask(env, "a")
		require.Zero(t, fired.Load())

		blockTask(env, "b")
		require.EqualValues(t, 1, fired.Load())

		blockTask(env, "c")
		require.EqualValues(t, 1, fired.Load(), "only the Nth finish fires it")
		require.Len(t, env.lines(t), 3, "every blocked task still gets its line")
	})

	t.Run("partial does not count", func(t *testing.T) {
		t.Parallel()

		env := newTrackerEnv(t)

		var fired atomic.Int32

		env.tr.SetBlockedThreshold(1, func() { fired.Add(1) })

		env.tr.Register("p", ModeList, intPtr(6))
		env.tr.PageDone("p", okPage(0, 20, 20))
		env.tr.PageDone("p", PageResult{Page: 1, Status: 429, Outcome: PageBlocked, Reason: ReasonHTTP429})
		env.tr.Finish("p")

		require.Zero(t, fired.Load())
		require.Equal(t, "partial", env.lines(t)[0]["status"])
	})

	t.Run("zero never fires", func(t *testing.T) {
		t.Parallel()

		env := newTrackerEnv(t)

		var fired atomic.Int32

		env.tr.SetBlockedThreshold(0, func() { fired.Add(1) })

		blockTask(env, "a")
		blockTask(env, "b")

		require.Zero(t, fired.Load())
	})

	t.Run("larger than the number of tasks never fires", func(t *testing.T) {
		t.Parallel()

		env := newTrackerEnv(t)

		var fired atomic.Int32

		env.tr.SetBlockedThreshold(10, func() { fired.Add(1) })

		blockTask(env, "a")
		blockTask(env, "b")

		require.Zero(t, fired.Load())
	})
}

func TestReportWriterOnErrorCalledOnce(t *testing.T) {
	t.Parallel()

	w, err := NewReportWriter(filepath.Join(t.TempDir(), "r.jsonl"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	var called atomic.Int32

	w.SetOnError(func() { called.Add(1) })

	require.Error(t, w.Write(SearchReport{ID: "a"}))
	require.Error(t, w.Write(SearchReport{ID: "b"}))
	require.EqualValues(t, 1, called.Load(), "only the first failure is announced")

	var nilWriter *ReportWriter

	nilWriter.SetOnError(func() {}) // harmless
}
