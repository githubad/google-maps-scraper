package gmaps

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// A name search that Google answers with the single-place layout is one result row,
// not an empty search (F006: the status probe's three searches all came back
// "empty" although each raw page held the business).
func TestSearchJobTracker_SinglePlacePageIsOneRow(t *testing.T) {
	t.Parallel()

	for _, file := range []string{
		"single_01_hooters-round-rock.json",
		"single_02_ztejas-avery-ranch.json",
		"single_03_trudys-north-star.json",
	} {
		t.Run(file, func(t *testing.T) {
			t.Parallel()

			env := newTrackerEnv(t)
			srv := statusServer(t, map[int]reply{0: {status: 200, body: loadListmodeFixture(t, file)}})
			job := trackedJob(t, srv.URL, 6, env)

			res, children, err := job.Process(context.Background(), fetchJob(t, job))

			require.NoError(t, err)
			require.Empty(t, children, "a single place has no next page")

			rows := flattenResult(res)
			require.Len(t, rows, 1)
			require.Equal(t, 1, rows[0].Rank)
			require.Equal(t, 0, rows[0].Page)
			require.Equal(t, "t_x", rows[0].ID, "the task id is the input id")

			line := env.lines(t)[0]
			require.Equal(t, "ok", line["status"])
			require.Nil(t, line["block_reason"])
			require.EqualValues(t, 1, line["rows_seen"])
			require.EqualValues(t, 1, line["results"])
			require.EqualValues(t, 1, line["pages_ok"])
			require.Equal(t, false, line["last_page_full"])
			require.Equal(t, []any{float64(200)}, line["http_statuses"])
		})
	}
}
