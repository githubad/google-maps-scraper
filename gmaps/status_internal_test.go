package gmaps

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizePlaceStatus(t *testing.T) {
	t.Parallel()

	for _, ok := range []string{PlaceOperational, PlaceClosedTemporarily, PlaceClosedPermanently} {
		require.Equal(t, ok, normalizePlaceStatus(ok))
	}

	for _, bad := range []string{"", "CLOSED", "closed_permanently", "Open ⋅ Closes 8 PM", "OPERATIONAL ", "Busy BBQ joint"} {
		require.Empty(t, normalizePlaceStatus(bad), bad)
	}

	require.Equal(t, "OPERATIONAL", PlaceOperational)
	require.Equal(t, "CLOSED_TEMPORARILY", PlaceClosedTemporarily)
	require.Equal(t, "CLOSED_PERMANENTLY", PlaceClosedPermanently)
}

// In task (jsonl) mode the result's status is one of the contract values or empty,
// whatever the page carried.
func TestSearchJobTracker_StatusIsAContractValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body []byte
		want []string
	}{
		{"a closed place", loadListmodeFixture(t, "single_01_hooters-round-rock.json"), []string{PlaceClosedPermanently}},
		{"an open single place", loadListmodeFixture(t, "control_01_franklin-barbecue-open.json"), []string{""}},
		{"open-now text on a stock page is not a status", outputPage(t), nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env := newTrackerEnv(t)
			srv := statusServer(t, map[int]reply{0: {status: 200, body: tc.body}})
			job := trackedJob(t, srv.URL, 1, env)

			res, _, err := job.Process(context.Background(), fetchJob(t, job))

			require.NoError(t, err)

			rows := flattenResult(res)
			require.NotEmpty(t, rows)

			for i, e := range rows {
				want := ""
				if tc.want != nil {
					want = tc.want[i]
				}

				require.Equal(t, want, e.Status, "%s row %d", e.Title, i)
			}
		})
	}
}
