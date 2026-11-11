package runner

import "testing"

func TestDefaultConcurrency(t *testing.T) {
	t.Parallel()

	tests := []struct {
		numCPU int
		want   int
	}{
		{0, 1},
		{1, 1},
		{2, 1},
		{3, 1},
		{4, 2},
		{7, 3},
		{16, 8},
	}

	for _, tc := range tests {
		if got := defaultConcurrency(tc.numCPU); got != tc.want {
			t.Errorf("defaultConcurrency(%d) = %d, want %d", tc.numCPU, got, tc.want)
		}
	}
}

func TestParseViewport(t *testing.T) {
	t.Parallel()

	for _, ok := range []string{"600x800", "800x600", "1920x889", "100x100", "8192x8192"} {
		if _, _, err := parseViewport(ok); err != nil {
			t.Errorf("parseViewport(%q): %v", ok, err)
		}
	}

	w, h, err := parseViewport("1920x889")
	if err != nil || w != 1920 || h != 889 {
		t.Errorf("parseViewport(1920x889) = %d, %d, %v", w, h, err)
	}

	for _, bad := range []string{"0x800", "99x800", "8193x800", "axb", "800", "800x", "x800", "-1x5", "600X800", ""} {
		if _, _, err := parseViewport(bad); err == nil {
			t.Errorf("parseViewport(%q) must fail", bad)
		}
	}
}
