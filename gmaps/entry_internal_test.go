package gmaps

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseReviewsRelativeDatePaths(t *testing.T) {
	tests := []struct {
		name    string
		wrapped bool
		path    []int
		when    string
	}{
		{
			name:    "inline review metadata path",
			wrapped: true,
			path:    []int{1, 6},
			when:    "7 months ago",
		},
		{
			name: "direct review fallback path",
			path: []int{3, 3},
			when: "3 weeks ago",
		},
		{
			name: "nested review rpc path",
			path: []int{2, 1, 3, 8, 0},
			when: "4 years ago",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			review := newReviewElement()
			setNested(review, tt.when, tt.path...)

			item := any(review)
			if tt.wrapped {
				item = []any{review}
			}

			reviews := parseReviews([]any{item})

			require.Len(t, reviews, 1)
			require.Equal(t, tt.when, reviews[0].When)
			require.Equal(t, "Ada Lovelace", reviews[0].Name)
			require.Equal(t, 5, reviews[0].Rating)
			require.Equal(t, "Clear review text", reviews[0].Description)
		})
	}
}

func TestParseReviewsPublishedAtFromMicrosecondTimestamp(t *testing.T) {
	review := newReviewElement()
	setNested(review, "6 months ago", 1, 6)
	setNested(review, 1764184138462529.0, 1, 2)

	reviews := parseReviews([]any{review})

	require.Len(t, reviews, 1)
	require.NotNil(t, reviews[0].PublishedAt)
	require.Equal(t, "2025-11-26T19:08:58.462529Z", reviews[0].PublishedAt.Format(time.RFC3339Nano))
}

func TestParseReviewsPublishedAtFallsBackToSecondTimestampPath(t *testing.T) {
	review := newReviewElement()
	setNested(review, "6 months ago", 1, 6)
	setNested(review, 1764184138462529.0, 1, 3)

	reviews := parseReviews([]any{review})

	require.Len(t, reviews, 1)
	require.NotNil(t, reviews[0].PublishedAt)
	require.Equal(t, "2025-11-26T19:08:58.462529Z", reviews[0].PublishedAt.Format(time.RFC3339Nano))
}

func TestParseReviewsPublishedAtRejectsInvalidTimestamps(t *testing.T) {
	tests := []struct {
		name   string
		micros float64
	}{
		{
			name: "missing",
		},
		{
			name:   "before lower bound",
			micros: float64(time.Date(2006, time.December, 31, 23, 59, 59, 0, time.UTC).UnixMicro()),
		},
		{
			name:   "too far in future",
			micros: float64(time.Now().UTC().Add(48 * time.Hour).UnixMicro()),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			review := newReviewElement()
			setNested(review, "6 months ago", 1, 6)

			if tt.micros != 0 {
				setNested(review, tt.micros, 1, 2)
			}

			reviews := parseReviews([]any{review})

			require.Len(t, reviews, 1)
			require.Nil(t, reviews[0].PublishedAt)
		})
	}
}

func TestParseReviewsSkipsItemsWithoutAuthor(t *testing.T) {
	review := make([]any, 4)
	setNested(review, "yesterday", 1, 6)
	setNested(review, 5.0, 2, 0, 0)

	reviews := parseReviews([]any{review})

	require.Empty(t, reviews)
}

func newReviewElement() []any {
	review := make([]any, 4)

	setNested(review, "Ada Lovelace", 1, 4, 5, 0)
	setNested(review, 5.0, 2, 0, 0)
	setNested(review, "Clear review text", 2, 15, 0, 0)

	return review
}

func setNested(root []any, value any, path ...int) {
	setNestedValue(root, value, path...)
}

func setNestedValue(current []any, value any, path ...int) []any {
	if len(path) == 0 {
		return current
	}

	index := path[0]
	current = ensureLen(current, index+1)

	if len(path) == 1 {
		current[index] = value

		return current
	}

	next, ok := current[index].([]any)
	if !ok {
		next = make([]any, path[1]+1)
	}

	current[index] = setNestedValue(next, value, path[1:]...)

	return current
}

func ensureLen(items []any, length int) []any {
	if len(items) >= length {
		return items
	}

	extended := make([]any, length)
	copy(extended, items)

	return extended
}

func TestEntryFromJSON_AddressOwnerUnchanged(t *testing.T) {
	darray := make([]any, 190)
	setNested(darray, "Boro", 183, 1, 0)
	setNested(darray, "Main St 1", 183, 1, 1)
	setNested(darray, "Austin", 183, 1, 3)
	setNested(darray, "78701", 183, 1, 4)
	setNested(darray, "TX", 183, 1, 5)
	setNested(darray, "US", 183, 1, 6)
	setNested(darray, "Owner Name", 57, 1)
	setNested(darray, "1234", 57, 2)

	wantAddr := Address{Borough: "Boro", Street: "Main St 1", City: "Austin", PostalCode: "78701", State: "TX", Country: "US"}
	wantOwner := Owner{ID: "1234", Name: "Owner Name", Link: "https://www.google.com/maps/contrib/1234"}

	// helpers used by both parsers
	require.Equal(t, wantAddr, parseCompleteAddress(darray))
	require.Equal(t, wantOwner, parseOwner(darray))
	require.Equal(t, Owner{}, parseOwner(make([]any, 3)))
	require.Equal(t, Address{}, parseCompleteAddress(nil))

	// EntryFromJSON keeps the same mapping
	raw, err := json.Marshal([]any{nil, nil, nil, nil, nil, nil, darray})
	require.NoError(t, err)

	entry, err := EntryFromJSON(raw)
	require.NoError(t, err)
	require.Equal(t, wantAddr, entry.CompleteAddress)
	require.Equal(t, wantOwner, entry.Owner)
}
