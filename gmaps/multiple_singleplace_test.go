package gmaps_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gosom/google-maps-scraper/gmaps"
)

// singlePlaceBody builds the stock layout with ONE item: data[0][1] = [item] and
// item[14] is the business. That is what Google answers when a search names one
// place (a name search such as "Hooters Round Rock"), instead of a list whose
// first item is a header and whose businesses are items 1 and up.
func singlePlaceBody(t *testing.T, row []any) []byte {
	t.Helper()

	item := make([]any, 15)
	item[14] = row

	raw, err := json.Marshal([]any{[]any{"q", []any{item}}})
	require.NoError(t, err)

	return raw
}

func TestParseSearchResults_SinglePlaceLayout(t *testing.T) {
	t.Run("one item with a business is one row", func(t *testing.T) {
		entries, err := gmaps.ParseSearchResults(singlePlaceBody(t, businessRow("Hooters", "0x1:0x2", "ChIJabc")))

		require.NoError(t, err)
		require.Len(t, entries, 1)
		require.Equal(t, "Hooters", entries[0].Title)
		require.Equal(t, "0x1:0x2", entries[0].DataID)
		require.Equal(t, "ChIJabc", entries[0].PlaceID)
	})

	t.Run("a header-only list is still empty", func(t *testing.T) {
		_, err := gmaps.ParseSearchResults([]byte(`[["q",[null]]]`))

		require.ErrorIs(t, err, gmaps.ErrEmptyBusinessList)
	})

	t.Run("one item without a business is still empty", func(t *testing.T) {
		item := make([]any, 15) // item[14] is nil
		raw, err := json.Marshal([]any{[]any{"q", []any{item}}})
		require.NoError(t, err)

		_, err = gmaps.ParseSearchResults(raw)

		require.ErrorIs(t, err, gmaps.ErrEmptyBusinessList)
	})

	t.Run("one item whose title is not a string is still empty", func(t *testing.T) {
		row := make([]any, 190)
		row[11] = 42.0

		_, err := gmaps.ParseSearchResults(singlePlaceBody(t, row))

		require.ErrorIs(t, err, gmaps.ErrEmptyBusinessList)
	})

	t.Run("two or more items are unchanged: the first is the header", func(t *testing.T) {
		entries, err := gmaps.ParseSearchResults(stockBody(t, businessRow("A", "0x1:0x1", "p1"), businessRow("B", "0x2:0x2", "p2")))

		require.NoError(t, err)
		require.Len(t, entries, 2)
		require.Equal(t, []string{"A", "B"}, []string{entries[0].Title, entries[1].Title})
	})
}

// The three pages below are real answers to name searches for businesses that have
// closed (saved by the F006 status probe, 2026-11-12): the page holds the single
// place, not a list.
func TestParseSearchResults_SinglePlaceFixtures(t *testing.T) {
	fixtures := []struct {
		file    string
		title   string
		address string
	}{
		{"single_01_hooters-round-rock.json", "Hooters", "2700 S I-35 Frontage Rd"},
		{"single_02_ztejas-avery-ranch.json", "Z'Tejas", "14900 Avery Ranch Blvd"},
		{"single_03_trudys-north-star.json", "Trudy's North Star", "8820 Burnet Rd"},
	}

	for _, f := range fixtures {
		t.Run(f.file, func(t *testing.T) {
			raw := readFixtureFile(t, "../testdata/listmode/"+f.file)

			entries, err := gmaps.ParseSearchResults(stripPrefix(raw))

			require.NoError(t, err)
			require.Len(t, entries, 1, "a single-place page is exactly one row")

			e := entries[0]
			require.Equal(t, f.title, e.Title)
			require.Contains(t, e.Address, f.address)
			require.NotEmpty(t, e.PlaceID)
			require.NotEmpty(t, e.DataID)
			require.NotEmpty(t, e.Cid)
			require.InDelta(t, 30.4, e.Latitude, 0.2)
			require.InDelta(t, -97.7, e.Longtitude, 0.2)
		})
	}
}
