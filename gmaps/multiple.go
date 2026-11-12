package gmaps

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	olc "github.com/google/open-location-code/go"
)

// ErrEmptyBusinessList is returned when a well-formed response has no
// businesses. The search report tells this apart from a parse failure.
var ErrEmptyBusinessList = errors.New("empty business list")

// ParseSearchResults parses a list-mode response body (without the leading
// ")]}'" line) into entries.
//
// Two layouts are accepted: the stock one, with the businesses at
// data[0][1][1:][14], and Chrome's, with them at data[64][i][1] (a business is
// the item at [1] whose item 11, the title, is a string). The stock layout is
// tried first; the Chrome layout is used only where the stock one has no rows.
func ParseSearchResults(raw []byte) ([]*Entry, error) {
	var data []any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("empty JSON data")
	}

	container, ok := data[0].([]any)
	if !ok || len(container) == 0 {
		return nil, fmt.Errorf("invalid business list structure")
	}

	items := getNthElementAndCast[[]any](container, 1)

	var businesses [][]any

	if len(items) >= 2 {
		businesses = stockBusinesses(items)
	} else {
		// one item: Google's answer to a search that names one place has no header,
		// the item is the business itself; otherwise it may be Chrome's layout
		businesses = singlePlaceBusiness(items)
		if len(businesses) == 0 {
			businesses = chromeBusinesses(data)
		}

		if len(businesses) == 0 {
			return nil, ErrEmptyBusinessList
		}
	}

	entries := make([]*Entry, 0, len(businesses))

	for _, business := range businesses {
		entries = append(entries, entryFromListRow(business))
	}

	return entries, nil
}

// stockBusinesses returns the business arrays of the stock layout (items[1:][14]).
func stockBusinesses(items []any) [][]any {
	businesses := make([][]any, 0, len(items)-1)

	for i := 1; i < len(items); i++ {
		arr, ok := items[i].([]any)
		if !ok {
			continue
		}

		businesses = append(businesses, getNthElementAndCast[[]any](arr, 14))
	}

	return businesses
}

// Place status values of the result record (docs/contracts.md §3).
const (
	PlaceOperational       = "OPERATIONAL"
	PlaceClosedTemporarily = "CLOSED_TEMPORARILY"
	PlaceClosedPermanently = "CLOSED_PERMANENTLY"
)

// closedMarker is the value of row[88][0] on a place Google shows as closed. On an
// open place that index holds a description snippet, a category or nothing, so only
// this exact string counts (F006 probe, 2026-11-12: "CLOSED" on three permanently
// closed places, none on 86 open rows).
const closedMarker = "CLOSED"

// closedStateOfRow returns PlaceClosedPermanently when the row carries the closed
// marker and "" otherwise. Every place observed with the marker was permanently
// closed; a temporarily closed place has not been observed, so
// PlaceClosedTemporarily is never produced here, and PlaceOperational is never
// inferred from the absence of the marker.
func closedStateOfRow(business []any) string {
	if getNthElementAndCast[string](business, 88, 0) == closedMarker {
		return PlaceClosedPermanently
	}

	return ""
}

// normalizePlaceStatus keeps a status only if it is one of the contract values; any
// other text (upstream's open-now line, for example) becomes "".
func normalizePlaceStatus(s string) string {
	switch s {
	case PlaceOperational, PlaceClosedTemporarily, PlaceClosedPermanently:
		return s
	default:
		return ""
	}
}

// singlePlaceBusiness returns the business of the single-place layout: data[0][1]
// holds exactly one item and its item 14 is the business (item 11, the title, is a
// string). That is what a name search such as "Hooters Round Rock" returns. A
// header-only list (one item without a business) gives nothing.
func singlePlaceBusiness(items []any) [][]any {
	if len(items) != 1 {
		return nil
	}

	item, ok := items[0].([]any)
	if !ok {
		return nil
	}

	business := getNthElementAndCast[[]any](item, 14)
	if _, isTitle := getNthElementAndCast[any](business, 11).(string); !isTitle {
		return nil
	}

	return [][]any{business}
}

// chromeBusinesses returns the business arrays of Chrome's layout: data[64][i][1]
// where item 11 (the title) is a string.
func chromeBusinesses(data []any) [][]any {
	const chromeListIndex = 64

	list := getNthElementAndCast[[]any](data, chromeListIndex)
	businesses := make([][]any, 0, len(list))

	for _, row := range list {
		arr, ok := row.([]any)
		if !ok {
			continue
		}

		business := getNthElementAndCast[[]any](arr, 1)
		if _, isTitle := getNthElementAndCast[any](business, 11).(string); !isTitle {
			continue
		}

		businesses = append(businesses, business)
	}

	return businesses
}

// entryFromListRow maps one list row to an Entry. The row has the same layout
// as the place data used by EntryFromJSON.
//
//nolint:mnd // it's ok, I need the indexes
func entryFromListRow(business []any) *Entry {
	var entry Entry

	entry.ID = getNthElementAndCast[string](business, 0)
	entry.Title = getNthElementAndCast[string](business, 11)
	entry.Categories = toStringSlice(getNthElementAndCast[[]any](business, 13))
	entry.WebSite = getNthElementAndCast[string](business, 7, 0)

	entry.ReviewRating = getNthElementAndCast[float64](business, 4, 7)
	entry.ReviewCount = int(getNthElementAndCast[float64](business, 4, 8))

	fullAddress := getNthElementAndCast[[]any](business, 2)

	entry.Address = func() string {
		sb := strings.Builder{}

		for i, part := range fullAddress {
			if i > 0 {
				sb.WriteString(", ")
			}

			fmt.Fprintf(&sb, "%v", part)
		}

		return sb.String()
	}()

	entry.Latitude = getNthElementAndCast[float64](business, 9, 2)
	entry.Longtitude = getNthElementAndCast[float64](business, 9, 3)
	entry.Phone = strings.ReplaceAll(getNthElementAndCast[string](business, 178, 0, 0), " ", "")
	entry.OpenHours = getHours(business)
	entry.Status = getNthElementAndCast[string](business, 34, 4, 4)

	if entry.Status == "" {
		entry.Status = closedStateOfRow(business)
	}

	entry.Timezone = getNthElementAndCast[string](business, 30)
	entry.DataID = getNthElementAndCast[string](business, 10)

	entry.PlusCode = olc.Encode(entry.Latitude, entry.Longtitude, 10)

	entry.PlaceID = getNthElementAndCast[string](business, 78)
	entry.Cid = cidFromDataID(entry.DataID)
	entry.CompleteAddress = parseCompleteAddress(business)
	entry.Owner = parseOwner(business)

	return &entry
}

func toStringSlice(arr []any) []string {
	ans := make([]string, 0, len(arr))
	for _, v := range arr {
		ans = append(ans, fmt.Sprintf("%v", v))
	}

	return ans
}
