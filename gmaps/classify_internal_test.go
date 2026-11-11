package gmaps

import "testing"

func TestIsSorryURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
		want bool
	}{
		{"sorry path", "https://www.google.com/sorry/index?continue=x", true},
		{"sorry path without query", "https://www.google.com/sorry/", true},
		{"search text sorry", "https://www.google.com/maps/search/sorry/@30.2,-97.7,14z", false},
		{"sorry only in the query", "https://www.google.com/search?q=/sorry/", false},
		{"path starts with sorry but no slash", "https://www.google.com/sorrynot", false},
		{"unparsable url with sorry path", "http://[::1/sorry/index", true},
		{"unparsable url without sorry", "http://[::1/maps", false},
		{"relative path", "/sorry/index", true},
		{"empty", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := isSorryURL(tc.url); got != tc.want {
				t.Fatalf("isSorryURL(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

func TestLocationIsSorry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   int
		location string
		want     bool
	}{
		{"302 to sorry", 302, "https://www.google.com/sorry/index", true},
		{"301 to sorry", 301, "/sorry/index?x=1", true},
		{"302 elsewhere", 302, "https://www.google.com/maps", false},
		{"200 with sorry location is ignored", 200, "https://www.google.com/sorry/index", false},
		{"302 without location", 302, "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := locationIsSorry(tc.status, tc.location); got != tc.want {
				t.Fatalf("locationIsSorry(%d, %q) = %v, want %v", tc.status, tc.location, got, tc.want)
			}
		})
	}
}
