package gmaps_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gosom/google-maps-scraper/gmaps"
)

// What the F006 probe found (2026-11-12). Three places that have closed for good
// answer a name search with a single-place page whose row[88][0] is the string
// "CLOSED" (and whose edit menu at row[96][5][0] says "Reopen this place"). An open
// place's row[88][0] is a description snippet or nil, and in list rows it is nil
// (or a snippet). Only the exact value "CLOSED" is a closed marker.

func statusRow(marker any) []any {
	row := businessRow("Acme", "0x1:0x2", "ChIJabc")
	if marker != nil {
		row[88] = []any{marker}
	}

	return row
}

func TestParseSearchResults_ClosedPlaceFixtures(t *testing.T) {
	for _, file := range []string{
		"single_01_hooters-round-rock.json",
		"single_02_ztejas-avery-ranch.json",
		"single_03_trudys-north-star.json",
	} {
		t.Run(file, func(t *testing.T) {
			entries, err := gmaps.ParseSearchResults(stripPrefix(readFixtureFile(t, "../testdata/listmode/"+file)))

			require.NoError(t, err)
			require.Len(t, entries, 1)
			require.Equal(t, gmaps.PlaceClosedPermanently, entries[0].Status)
		})
	}
}

func TestParseSearchResults_OpenPlaceFixturesHaveNoStatus(t *testing.T) {
	t.Run("an open single place (its row[88][0] is a description)", func(t *testing.T) {
		entries, err := gmaps.ParseSearchResults(stripPrefix(readFixtureFile(t, "../testdata/listmode/control_01_franklin-barbecue-open.json")))

		require.NoError(t, err)
		require.Len(t, entries, 1)
		require.Empty(t, entries[0].Status, "OPERATIONAL is never inferred, and a description is not a status")
	})

	t.Run("a list of open businesses", func(t *testing.T) {
		entries, err := gmaps.ParseSearchResults(stripPrefix(readFixtureFile(t, "../testdata/listmode/control_02_austin-roofing-list-open.json")))

		require.NoError(t, err)
		require.Len(t, entries, 4)

		for _, e := range entries {
			require.Empty(t, e.Status, e.Title)
		}
	})

	t.Run("the real list pages", func(t *testing.T) {
		for _, file := range []string{"har_01_roofing-contractor_o0.json", "har_02_roofing-contractor_o20.json", "har_03_roofing-contractor_o0.json"} {
			entries, err := gmaps.ParseSearchResults(stripPrefix(readFixtureFile(t, "../testdata/listmode/"+file)))

			require.NoError(t, err)

			for _, e := range entries {
				require.Empty(t, e.Status, "%s: %s", file, e.Title)
			}
		}
	})
}

func TestParseSearchResults_ClosedMarkerRules(t *testing.T) {
	tests := []struct {
		name   string
		marker any
		want   string
	}{
		{"CLOSED", "CLOSED", gmaps.PlaceClosedPermanently},
		{"nil", nil, ""},
		{"a description snippet", "Busy BBQ joint with a retro vibe", ""},
		{"a category string", "Supplier of building materials", ""},
		{"lower case is not the marker", "closed", ""},
		{"a text that merely contains the word", "Closed on Mondays", ""},
		{"a number", 1.0, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries, err := gmaps.ParseSearchResults(stockBody(t, statusRow(tc.marker)))

			require.NoError(t, err)
			require.Len(t, entries, 1)
			require.Equal(t, tc.want, entries[0].Status)
		})
	}
}

func TestParseSearchResults_StockOpenNowTextIsUntouched(t *testing.T) {
	// upstream's stock pages carry an open-now line at [34][4][4]; the list mapper keeps
	// copying it (stock behaviour); the contract enum is applied to jsonl tasks only
	row := statusRow("CLOSED")
	row[34] = []any{nil, nil, nil, nil, []any{nil, nil, nil, nil, "Open ⋅ Closes 8 PM"}}

	entries, err := gmaps.ParseSearchResults(stockBody(t, row))

	require.NoError(t, err)
	require.Equal(t, "Open ⋅ Closes 8 PM", entries[0].Status, "the stock text wins; the marker is only a fallback")
}
