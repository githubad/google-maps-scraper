package gmaps_test

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gosom/google-maps-scraper/gmaps"
)

// stripPrefix removes the ")]}'" first line the way the search job does.
func stripPrefix(b []byte) []byte {
	if bytes.HasPrefix(b, []byte(")]}'")) {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			return b[i+1:]
		}
	}

	return b
}

func readFixtureFile(t *testing.T, path string) []byte {
	t.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotEmpty(t, raw)

	return raw
}

// businessRow builds a synthetic list row (the array found at item 14 / item 1).
func businessRow(title, dataID, placeID string) []any {
	row := make([]any, 190)
	row[0] = "row-token"
	row[10] = dataID
	row[11] = title
	row[78] = placeID

	return row
}

// stockBody wraps rows in the stock layout: data[0][1][1:][14].
func stockBody(t *testing.T, rows ...[]any) []byte {
	t.Helper()

	items := make([]any, 1, 1+len(rows))

	for _, r := range rows {
		item := make([]any, 15)
		item[14] = r
		items = append(items, item)
	}

	raw, err := json.Marshal([]any{[]any{"q", items}})
	require.NoError(t, err)

	return raw
}

// chromeBody wraps rows in Chrome's layout: data[64][i][1], with no rows in data[0][1].
func chromeBody(t *testing.T, rows ...[]any) []byte {
	t.Helper()

	data := make([]any, 65)
	data[0] = []any{"q", []any{nil}}

	list := make([]any, 0, len(rows))
	for _, r := range rows {
		list = append(list, []any{nil, r})
	}

	data[64] = list

	raw, err := json.Marshal(data)
	require.NoError(t, err)

	return raw
}

func TestParseSearchResults_Fixture(t *testing.T) {
	raw := readFixtureFile(t, "../testdata/output.json")

	entries, err := gmaps.ParseSearchResults(raw)
	require.NoError(t, err)
	require.Len(t, entries, 20)

	first := entries[0]
	require.Equal(t, "Dream Coffee", first.Title)
	require.Equal(t, "ChIJoRVrMS-joRQRkzrMbbRUnBY", first.PlaceID)
	require.Equal(t, "0x14a1a32f316b15a1:0x169c54b46dcc3a93", first.DataID)
	require.Equal(t, "1629270299114224275", first.Cid)
	require.Equal(t, "Eakou 74", first.CompleteAddress.Street)
	require.Equal(t, "Ilion", first.CompleteAddress.City)
	require.Equal(t, "131 22", first.CompleteAddress.PostalCode)
	require.Equal(t, "GR", first.CompleteAddress.Country)
	require.Equal(t, "+302102616578", first.Phone)

	for i, e := range entries {
		require.NotEmpty(t, e.DataID, "entry %d", i)
		require.NotEmpty(t, e.Cid, "entry %d", i)
	}
}

func TestParseSearchResults_ChromeLayout(t *testing.T) {
	fixtures := []struct {
		file       string
		firstTitle string
	}{
		{"har_01_roofing-contractor_o0.json", "LOA Construction and Austin Roofing"},
		{"har_02_roofing-contractor_o20.json", "Papa Bear exteriors"},
		{"har_03_roofing-contractor_o0.json", "Austin Roofing Company & Handyman"},
	}

	seen := map[string]struct{}{}

	for i, fx := range fixtures {
		raw := readFixtureFile(t, "../testdata/listmode/"+fx.file)
		require.True(t, bytes.HasPrefix(raw, []byte(")]}'")), "fixtures keep the prefix line")

		entries, err := gmaps.ParseSearchResults(stripPrefix(raw))
		require.NoError(t, err, fx.file)
		require.Len(t, entries, 20, fx.file)
		require.Equal(t, fx.firstTitle, entries[0].Title, fx.file)

		for j, e := range entries {
			require.NotEmpty(t, e.Title, "%s row %d", fx.file, j)
			require.NotEmpty(t, e.DataID, "%s row %d", fx.file, j)
			require.NotEmpty(t, e.PlaceID, "%s row %d", fx.file, j)
			require.NotEmpty(t, e.Cid, "%s row %d", fx.file, j)

			if i < 2 {
				seen[e.DataID] = struct{}{}
			}
		}
	}

	require.Len(t, seen, 40, "o0 and o20 pages return different businesses")

	t.Run("stock layout still wins", func(t *testing.T) {
		entries, err := gmaps.ParseSearchResults(readFixtureFile(t, "../testdata/output.json"))
		require.NoError(t, err)
		require.Len(t, entries, 20)
		require.Equal(t, "Dream Coffee", entries[0].Title)
	})
}

func TestParseSearchResults_EdgeRows(t *testing.T) {
	t.Run("full row with owner and address", func(t *testing.T) {
		row := businessRow("Acme Roofing", "0x1:0x2", "ChIJabc")
		row[57] = []any{nil, "Owner Name", "1234"}
		row[183] = []any{nil, []any{"Borough", "Main St 1", nil, "Austin", "78701", "TX", "US"}}
		row[178] = []any{[]any{"+1 512 555 0100"}}

		entries, err := gmaps.ParseSearchResults(stockBody(t, row))
		require.NoError(t, err)
		require.Len(t, entries, 1)

		e := entries[0]
		require.Equal(t, "ChIJabc", e.PlaceID)
		require.Equal(t, "2", e.Cid)
		require.Equal(t, "+15125550100", e.Phone)
		require.Equal(t, "1234", e.Owner.ID)
		require.Equal(t, "Owner Name", e.Owner.Name)
		require.Equal(t, "https://www.google.com/maps/contrib/1234", e.Owner.Link)
		require.Equal(t, gmaps.Address{
			Borough: "Borough", Street: "Main St 1", City: "Austin",
			PostalCode: "78701", State: "TX", Country: "US",
		}, e.CompleteAddress)
	})

	t.Run("item 183 absent", func(t *testing.T) {
		entries, err := gmaps.ParseSearchResults(stockBody(t, businessRow("A", "0x1:0x2", "p1")))
		require.NoError(t, err)
		require.Equal(t, gmaps.Address{}, entries[0].CompleteAddress)
		require.Equal(t, gmaps.Owner{}, entries[0].Owner)
	})

	t.Run("item 183 empty or short does not panic", func(t *testing.T) {
		short := businessRow("A", "0x1:0x2", "p1")
		short[183] = []any{nil, []any{"Borough", "Street"}}

		empty := businessRow("B", "0x1:0x3", "p2")
		empty[183] = []any{}

		tiny := businessRow("C", "0x1:0x4", "p3")
		tiny[183] = []any{nil}

		require.NotPanics(t, func() {
			entries, err := gmaps.ParseSearchResults(stockBody(t, short, empty, tiny))
			require.NoError(t, err)
			require.Len(t, entries, 3)
			require.Equal(t, gmaps.Address{Borough: "Borough", Street: "Street"}, entries[0].CompleteAddress)
			require.Equal(t, gmaps.Address{}, entries[1].CompleteAddress)
			require.Equal(t, gmaps.Address{}, entries[2].CompleteAddress)
		})
	})

	t.Run("malformed or empty data_id gives empty cid and the row is kept", func(t *testing.T) {
		entries, err := gmaps.ParseSearchResults(stockBody(t,
			businessRow("A", "garbage", "p1"),
			businessRow("B", "", "p2"),
		))
		require.NoError(t, err)
		require.Len(t, entries, 2)
		require.Empty(t, entries[0].Cid)
		require.Empty(t, entries[1].Cid)
		require.Equal(t, "p2", entries[1].PlaceID)
	})

	t.Run("chrome layout row carries the same fields", func(t *testing.T) {
		row := businessRow("Chrome Co", "0x1:0xff", "ChIJchrome")
		row[183] = []any{nil, []any{nil, "Street 9", nil, "City", "11111", "ST", "CC"}}

		entries, err := gmaps.ParseSearchResults(chromeBody(t, row))
		require.NoError(t, err)
		require.Len(t, entries, 1)
		require.Equal(t, "Chrome Co", entries[0].Title)
		require.Equal(t, "255", entries[0].Cid)
		require.Equal(t, "ChIJchrome", entries[0].PlaceID)
		require.Equal(t, "Street 9", entries[0].CompleteAddress.Street)
		require.Equal(t, "11111", entries[0].CompleteAddress.PostalCode)
	})
}

func TestParseSearchResults_NoRows(t *testing.T) {
	notAString := make([]any, 190)
	notAString[11] = 42.0

	noRows := map[string][]byte{
		"empty items":     []byte(`[["q",[]]]`),
		"header only":     []byte(`[["q",[null]]]`),
		"chrome no title": chromeBody(t, notAString),
	}

	for name, body := range noRows {
		t.Run(name, func(t *testing.T) {
			_, err := gmaps.ParseSearchResults(body)
			require.Error(t, err)
			require.Contains(t, err.Error(), "empty business list")
		})
	}

	t.Run("empty container is unchanged", func(t *testing.T) {
		_, err := gmaps.ParseSearchResults([]byte(`[[]]`))
		require.ErrorContains(t, err, "invalid business list structure")
	})

	t.Run("invalid json is unchanged", func(t *testing.T) {
		_, err := gmaps.ParseSearchResults([]byte(`{not json`))
		require.ErrorContains(t, err, "failed to unmarshal JSON")
	})
}
