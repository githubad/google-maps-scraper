package gmaps

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/gosom/scrapemate"
	"github.com/stretchr/testify/require"
)

// everywhere is a radius (metres) large enough to keep every row in the stock radius filter.
const everywhere = 4e7

func withPrefix(body []byte) []byte {
	return append([]byte(")]}'\n"), body...)
}

func loadOutputFixture(t *testing.T) []byte {
	t.Helper()

	raw, err := os.ReadFile("../testdata/output.json")
	require.NoError(t, err)
	require.NotEmpty(t, raw)

	return raw
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}

	return ""
}

func asFloat(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}

	return 0
}

// at walks a decoded JSON value by indexes, returning nil when a step is missing.
func at(v any, idx ...int) any {
	for _, i := range idx {
		arr, ok := v.([]any)
		if !ok || i >= len(arr) {
			return nil
		}

		v = arr[i]
	}

	return v
}

// fixtureRows returns the 20 business rows (item 14 of each item) of output.json.
func fixtureRows(t *testing.T) []any {
	t.Helper()

	var data []any

	require.NoError(t, json.Unmarshal(loadOutputFixture(t), &data))

	items, ok := at(data, 0, 1).([]any)
	require.True(t, ok)

	rows := make([]any, 0, len(items)-1)
	for _, it := range items[1:] {
		rows = append(rows, at(it, 14))
	}

	return rows
}

func newFixtureJob(opts ...SearchJobOptions) *SearchJob {
	return NewSearchJob(&MapSearchParams{
		Query:    "coffee",
		Hl:       "en",
		Location: MapLocation{Radius: everywhere},
	}, opts...)
}

func processFixture(t *testing.T, job *SearchJob) (any, []scrapemate.IJob, error) {
	t.Helper()

	return job.Process(context.Background(), &scrapemate.Response{Body: withPrefix(loadOutputFixture(t))})
}

func TestSearchJobProcess_TaskIDSetsInputID(t *testing.T) {
	job := newFixtureJob(WithSearchJobTaskID("t_x"))

	res, _, err := processFixture(t, job)
	require.NoError(t, err)

	entries, ok := res.([]*Entry)
	require.True(t, ok, "C2 keeps the stock result type; flattening comes with jsonl mode")
	require.Len(t, entries, 20)

	for _, e := range entries {
		require.Equal(t, "t_x", e.ID)
		require.NotEmpty(t, e.PlaceID)
		require.Equal(t, "https://www.google.com/maps/place/?q=place_id:"+e.PlaceID, e.Link)
	}
}

func TestSearchJobProcess_StockKeepsRowID(t *testing.T) {
	rows := fixtureRows(t)
	rowIDs := map[string]struct{}{}

	for _, r := range rows {
		id := asString(at(r, 0))
		require.NotEmpty(t, id)

		rowIDs[id] = struct{}{}
	}

	res, _, err := processFixture(t, newFixtureJob())
	require.NoError(t, err)
	require.IsType(t, []*Entry{}, res)

	entries, ok := res.([]*Entry)
	require.True(t, ok)
	require.Len(t, entries, 20)

	for _, e := range entries {
		_, ok := rowIDs[e.ID]
		require.True(t, ok, "input_id %q must be upstream's row item 0", e.ID)
	}
}

func TestStockModeAdditiveFields(t *testing.T) {
	rows := fixtureRows(t)

	byData := map[string]any{}

	for _, r := range rows {
		id := asString(at(r, 10))
		byData[id] = r
	}

	res, _, err := processFixture(t, newFixtureJob())
	require.NoError(t, err)

	entries, ok := res.([]*Entry)
	require.True(t, ok)
	require.Len(t, entries, 20)

	for _, e := range entries {
		row, ok := byData[e.DataID]
		require.True(t, ok, "entry %q has a source row", e.DataID)

		// additive fields are filled
		require.NotEmpty(t, e.PlaceID)
		require.NotEmpty(t, e.Cid)

		// values upstream already filled still come from the same row items
		title := asString(at(row, 11))
		require.Equal(t, title, e.Title)

		rowID := asString(at(row, 0))
		require.Equal(t, rowID, e.ID)

		lat := asFloat(at(row, 9, 2))
		lon := asFloat(at(row, 9, 3))

		require.Equal(t, lat, e.Latitude)
		require.Equal(t, lon, e.Longtitude)

		reviews := asFloat(at(row, 4, 8))
		require.Equal(t, int(reviews), e.ReviewCount)

		rating := asFloat(at(row, 4, 7))
		require.Equal(t, rating, e.ReviewRating)
	}

	var dream *Entry

	for _, e := range entries {
		if e.DataID == "0x14a1a32f316b15a1:0x169c54b46dcc3a93" {
			dream = e
		}
	}

	require.NotNil(t, dream)
	require.Equal(t, "1629270299114224275", dream.Cid)
	require.Equal(t, "131 22", dream.CompleteAddress.PostalCode)
	require.Equal(t, 0, dream.Page)
	require.Equal(t, 0, dream.Rank)
}
