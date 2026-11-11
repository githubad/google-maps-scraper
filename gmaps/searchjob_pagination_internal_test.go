package gmaps

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gosom/scrapemate"
	"github.com/gosom/scrapemate/adapters/providers/memory"
	"github.com/stretchr/testify/require"

	"github.com/gosom/google-maps-scraper/exiter"
)

func outputPage(t *testing.T) []byte {
	t.Helper()

	return withPrefix(loadOutputFixture(t))
}

func TestListPagination_ShortLastPage(t *testing.T) {
	t.Run("fixture twice then 5 rows (AS-3)", func(t *testing.T) {
		full := outputPage(t)
		short := trimRows(t, full)

		srv, hits := newBodyServer(t, func(offset int) []byte {
			if offset >= 40 {
				return short
			}

			return full
		})

		run := runTask(t, newPagedJob(srv.URL, 6))

		require.Equal(t, []int{0, 20, 40}, hits.Offsets(), "exactly three requests")
		require.Empty(t, run.Errors)
		require.Len(t, run.Entries, 20, "duplicates are dropped")
		require.Equal(t, sequence(20), ranks(run.Entries))
	})

	t.Run("three distinct pages", func(t *testing.T) {
		p0 := loadListmodeFixture(t, "har_01_roofing-contractor_o0.json")
		p1 := loadListmodeFixture(t, "har_02_roofing-contractor_o20.json")
		p2 := trimRows(t, loadListmodeFixture(t, "har_03_roofing-contractor_o0.json"))

		srv, hits := newBodyServer(t, func(offset int) []byte {
			switch offset {
			case 0:
				return p0
			case 20:
				return p1
			case 40:
				return p2
			default:
				return nil
			}
		})

		unique := map[string]struct{}{}

		for _, body := range [][]byte{p0, p1, p2} {
			for _, e := range parseServed(t, body) {
				unique[e.DataID] = struct{}{}
			}
		}

		run := runTask(t, newPagedJob(srv.URL, 6))

		require.Equal(t, []int{0, 20, 40}, hits.Offsets())
		require.Empty(t, run.Errors)
		require.Len(t, run.Entries, len(unique))
		require.Equal(t, sequence(len(unique)), ranks(run.Entries), "continuous ranks")

		seen := map[string]struct{}{}
		lastPage := 0

		for _, e := range run.Entries {
			_, dup := seen[e.DataID]
			require.False(t, dup, "duplicate %s", e.DataID)

			seen[e.DataID] = struct{}{}

			require.GreaterOrEqual(t, e.Page, lastPage, "pages are non-decreasing")
			lastPage = e.Page
		}

		require.GreaterOrEqual(t, lastPage, 1, "later pages contribute rows")
	})
}

func TestListPagination_IdenticalPages(t *testing.T) {
	full := outputPage(t)
	srv, hits := newBodyServer(t, func(int) []byte { return full })

	run := runTask(t, newPagedJob(srv.URL, 3))

	require.Equal(t, []int{0, 20, 40}, hits.Offsets(), "pagination follows raw rows, not unique results (AS-4)")
	require.Len(t, run.Entries, 20)
	require.Equal(t, sequence(20), ranks(run.Entries))

	for _, e := range run.Entries {
		require.Equal(t, 0, e.Page, "pages 1 and 2 contribute no records")
	}
}

func TestListPagination_MaxPagesOne(t *testing.T) {
	full := outputPage(t)
	srv, hits := newBodyServer(t, func(int) []byte { return full })

	run := runTask(t, newPagedJob(srv.URL, 1))

	require.Equal(t, []int{0}, hits.Offsets(), "exactly one request (AS-5)")
	require.Len(t, run.Entries, 20)
}

func TestListPagination_StopsAtMaxPages(t *testing.T) {
	pages := [][]byte{
		loadListmodeFixture(t, "har_01_roofing-contractor_o0.json"),
		loadListmodeFixture(t, "har_02_roofing-contractor_o20.json"),
		loadListmodeFixture(t, "har_03_roofing-contractor_o0.json"),
	}

	serve := func(offset int) []byte { return pages[(offset/20)%len(pages)] }

	t.Run("six full pages", func(t *testing.T) {
		srv, hits := newBodyServer(t, serve)

		runTask(t, newPagedJob(srv.URL, 6))

		require.Equal(t, []int{0, 20, 40, 60, 80, 100}, hits.Offsets())
	})

	t.Run("zero-value job fetches one page", func(t *testing.T) {
		srv, hits := newBodyServer(t, serve)

		job := NewSearchJob(&MapSearchParams{Query: "coffee", Hl: "en", Location: MapLocation{Radius: everywhere}},
			WithSearchJobBaseURL(srv.URL))

		run := runTask(t, job)

		require.Equal(t, []int{0}, hits.Offsets())
		require.Len(t, run.Entries, 20)

		for _, e := range run.Entries {
			require.Equal(t, 0, e.Rank, "stock jobs do not rank")
		}
	})

	t.Run("max pages 0 and 1 on a task", func(t *testing.T) {
		for _, n := range []int{0, 1} {
			srv, hits := newBodyServer(t, serve)

			runTask(t, newPagedJob(srv.URL, n))

			require.Equal(t, []int{0}, hits.Offsets(), "max_pages %d", n)
		}
	})
}

func TestListPagination_ShortFirstPage(t *testing.T) {
	short := trimRows(t, outputPage(t))
	srv, hits := newBodyServer(t, func(int) []byte { return short })

	run := runTask(t, newPagedJob(srv.URL, 6))

	require.Equal(t, []int{0}, hits.Offsets(), "fewer than 20 rows on page 0: no further pages")
	require.Equal(t, sequence(5), ranks(run.Entries))
}

func TestListPagination_UnparseablePage(t *testing.T) {
	full := outputPage(t)

	bodies := map[string][]byte{
		"bad json":       []byte(")]}'\n{not json"),
		"prefix only":    []byte(")]}'\n"),
		"empty response": []byte(""),
	}

	for name, bad := range bodies {
		t.Run(name, func(t *testing.T) {
			srv, hits := newBodyServer(t, func(offset int) []byte {
				if offset == 0 {
					return full
				}

				return bad
			})

			run := runTask(t, newPagedJob(srv.URL, 6))

			require.Equal(t, []int{0, 20}, hits.Offsets())
			require.Len(t, run.Entries, 20, "earlier pages are kept")
			require.Len(t, run.Errors, 1)
			require.NotContains(t, run.Errors[0].Error(), srv.URL, "errors do not echo the request URL")
		})
	}
}

func TestListPagination_FetchErrorPage(t *testing.T) {
	full := outputPage(t)
	mon := &recordingExiter{}

	job := newPagedJob("http://unused.invalid", 6, WithSearchJobExitMonitor(mon))

	res, next, err := job.Process(context.Background(), &scrapemate.Response{Body: full})
	require.NoError(t, err)
	require.Len(t, flattenResult(res), 20)
	require.Len(t, next, 1)

	_, more, err := next[0].Process(context.Background(), &scrapemate.Response{Error: errors.New("boom")})
	require.Error(t, err)
	require.Empty(t, more)
	require.Equal(t, 1, mon.seedsCompleted, "the task completes exactly once, on the failed page")
}

func TestTaskStateAdmit(t *testing.T) {
	t.Parallel()

	t.Run("key order and ranks", func(t *testing.T) {
		t.Parallel()

		s := newTaskState()

		rank, ok := s.admit(&Entry{DataID: "d1", PlaceID: "p1"})
		require.True(t, ok)
		require.Equal(t, 1, rank)

		_, ok = s.admit(&Entry{DataID: "d1", PlaceID: "other"})
		require.False(t, ok, "same data_id is a duplicate")

		rank, ok = s.admit(&Entry{DataID: "d2"})
		require.True(t, ok)
		require.Equal(t, 2, rank, "dropped rows do not consume a rank")

		rank, ok = s.admit(&Entry{PlaceID: "p9"})
		require.True(t, ok)
		require.Equal(t, 3, rank)

		_, ok = s.admit(&Entry{PlaceID: "p9", Title: "different"})
		require.False(t, ok, "without data_id the place_id decides")

		rank, ok = s.admit(&Entry{Title: "Acme", Address: "1 Main St"})
		require.True(t, ok)
		require.Equal(t, 4, rank)

		_, ok = s.admit(&Entry{Title: "Acme", Address: "1 Main St"})
		require.False(t, ok, "without ids title|address decides")

		_, ok = s.admit(&Entry{Title: "Acme", Address: "2 Main St"})
		require.True(t, ok)
	})

	t.Run("concurrent callers get unique ranks", func(t *testing.T) {
		t.Parallel()

		s := newTaskState()

		var (
			wg    sync.WaitGroup
			mu    sync.Mutex
			ranks = map[int]struct{}{}
		)

		for i := range 50 {
			wg.Add(1)

			go func() {
				defer wg.Done()

				rank, ok := s.admit(&Entry{DataID: "d" + strings.Repeat("x", i)})
				if ok {
					mu.Lock()
					ranks[rank] = struct{}{}
					mu.Unlock()
				}
			}()
		}

		wg.Wait()
		require.Len(t, ranks, 50)
	})
}

func TestListPagination_RankAfterRadiusFilter(t *testing.T) {
	parsed := parseServed(t, outputPage(t))
	center := parsed[0]

	distances := make([]float64, len(parsed))
	for i, e := range parsed {
		distances[i] = e.haversineDistance(center.Latitude, center.Longtitude)
	}

	radius := distances[0]
	for _, d := range distances {
		if d > radius && d < 800 { // some rows are within 800 m of the first one
			radius = d
		}
	}

	if radius == 0 {
		radius = 800
	}

	var want []string

	for i, e := range parsed {
		if distances[i] <= radius {
			want = append(want, e.DataID)
		}
	}

	require.Less(t, len(want), 20, "the radius must drop some rows")

	full := outputPage(t)
	srv, _ := newBodyServer(t, func(int) []byte { return full })

	job := NewSearchJob(&MapSearchParams{
		Query: "coffee", Hl: "en",
		Location: MapLocation{Lat: center.Latitude, Lon: center.Longtitude, Radius: radius},
	}, WithSearchJobTaskID("t_x"), WithSearchJobMaxPages(1), WithSearchJobBaseURL(srv.URL))

	run := runTask(t, job)

	got := make([]string, 0, len(run.Entries))
	for _, e := range run.Entries {
		got = append(got, e.DataID)
	}

	require.Equal(t, want, got, "Google's order, far rows dropped")
	require.Equal(t, sequence(len(want)), ranks(run.Entries), "ranks are assigned after filtering")
}

func TestListPagination_NextPageJob(t *testing.T) {
	full := outputPage(t)
	job := newPagedJob("http://unused.invalid", 6)

	require.Equal(t, scrapemate.PriorityMedium, job.Priority, "new tasks keep the default priority")

	_, next, err := job.Process(context.Background(), &scrapemate.Response{Body: full})
	require.NoError(t, err)
	require.Len(t, next, 1, "pages of a task are fetched one after the other")

	child, ok := next[0].(*SearchJob)
	require.True(t, ok)
	require.Equal(t, scrapemate.PriorityHigh, child.Priority, "page jobs outrank new tasks")
	require.Equal(t, "t_x", child.TaskID)
	require.Equal(t, 1, child.Page)
	require.Equal(t, 6, child.MaxPages)
	require.Same(t, job.State, child.State)
	require.NotEqual(t, job.ID, child.ID)
	require.Equal(t, job.URL, child.URL)
	require.Contains(t, child.URLParams["pb"], "!7i20!8i20")
	require.Equal(t, "coffee", child.URLParams["q"])
	require.Equal(t, "en", child.URLParams["hl"])
	require.Contains(t, job.URLParams["pb"], "!7i20!8i0", "page 0 keeps !8i0")

	_, again, err := child.Process(context.Background(), &scrapemate.Response{Body: full})
	require.NoError(t, err)
	require.Len(t, again, 1)

	grandchild, ok := again[0].(*SearchJob)
	require.True(t, ok)
	require.Contains(t, grandchild.URLParams["pb"], "!7i20!8i40")
	require.Equal(t, 2, grandchild.Page)

	short := trimRows(t, full)

	_, last, err := grandchild.Process(context.Background(), &scrapemate.Response{Body: short})
	require.NoError(t, err)
	require.Empty(t, last, "a short page ends the task")
}

func TestSeedCompletedOnlyOnLastPage(t *testing.T) {
	p0 := loadListmodeFixture(t, "har_01_roofing-contractor_o0.json")
	p1 := loadListmodeFixture(t, "har_02_roofing-contractor_o20.json")
	p2 := trimRows(t, loadListmodeFixture(t, "har_03_roofing-contractor_o0.json"))

	unique := map[string]struct{}{}

	for _, body := range [][]byte{p0, p1, p2} {
		for _, e := range parseServed(t, body) {
			unique[e.DataID] = struct{}{}
		}
	}

	want := len(unique) // the short last page repeats rows seen earlier

	run := func(t *testing.T, opts ...SearchJobOptions) *recordingExiter {
		t.Helper()

		mon := &recordingExiter{}
		job := newPagedJob("http://unused.invalid", 6, append(opts, WithSearchJobExitMonitor(mon))...)

		var current scrapemate.IJob = job

		for i, body := range [][]byte{p0, p1, p2} {
			_, next, err := current.Process(context.Background(), &scrapemate.Response{Body: body})
			require.NoError(t, err)

			if i < 2 {
				require.Equal(t, 0, mon.seedsCompleted, "no seed completes before the last page (page %d)", i)
				require.Len(t, next, 1)

				current = next[0]
			}
		}

		return mon
	}

	t.Run("seed once, places per page", func(t *testing.T) {
		mon := run(t)

		require.Equal(t, 1, mon.seedsCompleted)
		require.Equal(t, want, mon.placesFound, "records after dedup")
		require.Equal(t, want, mon.placesDone)
	})

	t.Run("writer managed completion leaves places to the writer", func(t *testing.T) {
		mon := run(t, WithSearchJobWriterManagedCompletion())

		require.Equal(t, 1, mon.seedsCompleted)
		require.Equal(t, want, mon.placesFound)
		require.Zero(t, mon.placesDone)
	})

	t.Run("single page task behaves as today", func(t *testing.T) {
		mon := &recordingExiter{}
		job := newPagedJob("http://unused.invalid", 1, WithSearchJobExitMonitor(mon))

		_, next, err := job.Process(context.Background(), &scrapemate.Response{Body: p0})
		require.NoError(t, err)
		require.Empty(t, next)
		require.Equal(t, 1, mon.seedsCompleted)
		require.Equal(t, []string{"found", "seed", "done"}, mon.events, "same call order as before")
	})
}

func TestExiterNeverFiresMidTask(t *testing.T) {
	full := outputPage(t)
	short := trimRows(t, full)

	srv, _ := newBodyServer(t, func(offset int) []byte {
		if offset >= 20 {
			return short
		}

		return full
	})

	var fired atomic.Bool

	done := make(chan struct{})
	mon := exiter.New()
	mon.SetSeedCount(2)
	mon.SetCancelFunc(func() {
		fired.Store(true)
		close(done)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go mon.Run(ctx)

	taskA := newPagedJob(srv.URL, 6, WithSearchJobExitMonitor(mon))
	runTask(t, taskA)

	// task B: page 0 only, its next page is still pending
	taskB := newPagedJob(srv.URL, 6, WithSearchJobExitMonitor(mon))

	_, next, err := taskB.Process(context.Background(), fetchJob(t, taskB))
	require.NoError(t, err)
	require.Len(t, next, 1)

	select {
	case <-done:
		t.Fatal("the monitor finished while a task was between pages")
	case <-time.After(200 * time.Millisecond):
	}

	require.False(t, fired.Load())

	lastJob, isSearchJob := next[0].(*SearchJob)
	require.True(t, isSearchJob)

	_, rest, err := lastJob.Process(context.Background(), fetchJob(t, lastJob))
	require.NoError(t, err)
	require.Empty(t, rest)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the monitor did not finish after the last page")
	}
}

func TestPageJobsOutrankSeedsAndGapIsSmall(t *testing.T) {
	newJob := func(id string, prio int) *SearchJob {
		j := NewSearchJob(&MapSearchParams{Query: id, Hl: "en"})
		j.ID = id
		j.Priority = prio

		return j
	}

	t.Run("page jobs outrank new tasks", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			provider := memory.New()
			ctx, cancel := context.WithCancel(t.Context())

			for _, id := range []string{"s1", "s2", "s3", "s4", "s5"} {
				require.NoError(t, provider.Push(ctx, newJob(id, scrapemate.PriorityMedium)))
			}

			require.NoError(t, provider.Push(ctx, newJob("page", scrapemate.PriorityHigh)))

			synctest.Wait() // every pusher is parked on its channel

			jobs, _ := provider.Jobs(ctx)

			got := make([]string, 0, 6)
			for range 6 {
				got = append(got, (<-jobs).GetID())
			}

			cancel()
			synctest.Wait()

			require.Equal(t, "page", got[0], "the page job is delivered before not-yet-started tasks")
		})
	})

	t.Run("the hand-off between two pages is far below the shortest inactivity window", func(t *testing.T) {
		const shortestInactivity = time.Minute // the shortest -exit-on-inactivity in use (contract §1)

		provider := memory.New()
		ctx, cancel := context.WithCancel(context.Background())

		defer cancel()

		jobs, _ := provider.Jobs(ctx)

		start := time.Now()

		require.NoError(t, provider.Push(ctx, newJob("page", scrapemate.PriorityHigh)))
		require.Equal(t, "page", (<-jobs).GetID())

		require.Less(t, time.Since(start), shortestInactivity/60)
	})
}
