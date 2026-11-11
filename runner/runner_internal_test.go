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
