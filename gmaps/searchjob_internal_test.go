package gmaps

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
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

	items, ok := res.([]any)
	require.True(t, ok, "task jobs return flat results")
	require.Len(t, items, 20)

	for _, item := range items {
		e, isEntry := item.(*Entry)
		require.True(t, isEntry)
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

func TestSearchJobProcess_FlatResultsWhenTaskID(t *testing.T) {
	res, _, err := processFixture(t, newFixtureJob(WithSearchJobTaskID("t_x")))
	require.NoError(t, err)

	items, ok := res.([]any)
	require.True(t, ok, "task jobs return a flat []any so the stock jsonwriter writes one object per line")
	require.Len(t, items, 20)

	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)

	for _, item := range items {
		entry, isEntry := item.(*Entry)
		require.True(t, isEntry)
		require.NoError(t, enc.Encode(entry))
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 20)

	for _, line := range lines {
		require.True(t, strings.HasPrefix(line, "{"), line)
		require.Contains(t, line, `"input_id":"t_x"`)
	}

	stock, _, err := processFixture(t, newFixtureJob())
	require.NoError(t, err)
	require.IsType(t, []*Entry{}, stock, "stock jobs keep []*Entry")
}

func TestSearchJobProcess_RadiusRules(t *testing.T) {
	parsed, err := ParseSearchResults(loadOutputFixture(t))
	require.NoError(t, err)

	dataIDs := func(entries []*Entry) []string {
		ids := make([]string, 0, len(entries))
		for _, e := range entries {
			ids = append(ids, e.DataID)
		}

		return ids
	}

	flatIDs := func(res any) []string {
		items, ok := res.([]any)
		require.True(t, ok)

		ids := make([]string, 0, len(items))

		for _, item := range items {
			entry, isEntry := item.(*Entry)
			require.True(t, isEntry)

			ids = append(ids, entry.DataID)
		}

		return ids
	}

	center := parsed[0]
	params := func(radius float64) *MapSearchParams {
		return &MapSearchParams{
			Query: "coffee", Hl: "en",
			Location: MapLocation{Lat: center.Latitude, Lon: center.Longtitude, Radius: radius},
		}
	}

	t.Run("no radius filter keeps every row in Google's order", func(t *testing.T) {
		job := NewSearchJob(params(0), WithSearchJobNoRadiusFilter())

		res, _, err := processFixture(t, job)
		require.NoError(t, err)

		entries, ok := res.([]*Entry)
		require.True(t, ok)
		require.Equal(t, dataIDs(parsed), dataIDs(entries))
	})

	distances := make([]float64, 0, len(parsed))
	for _, e := range parsed {
		distances = append(distances, e.haversineDistance(center.Latitude, center.Longtitude))
	}

	sorted := append([]float64(nil), distances...)
	slices.Sort(sorted)

	radius := sorted[9] // the ten nearest rows (ties may add a few) stay

	var wantOrdered []string

	for i, e := range parsed {
		if distances[i] <= radius {
			wantOrdered = append(wantOrdered, e.DataID)
		}
	}

	require.Greater(t, len(wantOrdered), 0)
	require.Less(t, len(wantOrdered), 20)

	t.Run("task job drops far rows but keeps Google's order", func(t *testing.T) {
		job := NewSearchJob(params(radius), WithSearchJobTaskID("t_x"))

		res, _, err := processFixture(t, job)
		require.NoError(t, err)
		require.Equal(t, wantOrdered, flatIDs(res))
	})

	t.Run("stock job keeps the distance-sorted filter", func(t *testing.T) {
		job := NewSearchJob(params(radius))

		res, _, err := processFixture(t, job)
		require.NoError(t, err)

		entries, ok := res.([]*Entry)
		require.True(t, ok)

		want := filterAndSortEntriesWithinRadius(parsed, center.Latitude, center.Longtitude, radius)
		require.Equal(t, dataIDs(want), dataIDs(entries))
	})
}

func TestBuildGoogleMapsParams_Offset(t *testing.T) {
	stock := buildGoogleMapsParams(&MapSearchParams{Query: "q", Hl: "en"})["pb"]
	require.Contains(t, stock, "!7i20!8i0!10b1", "stock requests keep !8i0")

	for _, offset := range []int{20, 40} {
		pb := buildGoogleMapsParams(&MapSearchParams{Query: "q", Hl: "en", Offset: offset})["pb"]
		require.Contains(t, pb, "!7i20!8i"+strconv.Itoa(offset)+"!10b1")
	}
}

func TestListPagination_QueryRoundTrip(t *testing.T) {
	long := strings.Repeat("té ñ x", 33)[:198]
	full := withPrefix(loadOutputFixture(t))

	for _, q := range []string{"techos en Bogotá", long} {
		srv, hits := newBodyServer(t, func(int) []byte { return full })

		job := NewSearchJob(&MapSearchParams{Query: q, Hl: "es", Location: MapLocation{Radius: everywhere}},
			WithSearchJobTaskID("t_x"), WithSearchJobMaxPages(2), WithSearchJobBaseURL(srv.URL))

		runTask(t, job)

		require.Equal(t, []string{q, q}, hits.Queries(), "q round-trips exactly on every page")
		require.Equal(t, []string{"es", "es"}, hits.Langs())
	}
}
