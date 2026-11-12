package filerunner_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func resultRecordsOf(t *testing.T, dir string) []map[string]any {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(dir, "results.jsonl"))
	require.NoError(t, err)

	var records []map[string]any

	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if l == "" || l == "null" {
			continue
		}

		var m map[string]any

		require.NoError(t, json.Unmarshal([]byte(l), &m))

		records = append(records, m)
	}

	return records
}

// The business status of the result record (docs/contracts.md §3): the closed
// marker of a single-place page becomes CLOSED_PERMANENTLY; an open place has none.
func TestStatusE2E(t *testing.T) {
	tests := []struct {
		fixture string
		title   string
		want    string
	}{
		{"single_02_ztejas-avery-ranch.json", "Z'Tejas", "CLOSED_PERMANENTLY"},
		{"single_03_trudys-north-star.json", "Trudy's North Star", "CLOSED_PERMANENTLY"},
		{"control_01_franklin-barbecue-open.json", "Franklin Barbecue", ""},
	}

	for _, tc := range tests {
		t.Run(tc.title, func(t *testing.T) {
			body := readFixture(t, tc.fixture)

			res := runBatch(t, []string{onePageTask()}, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(body)
			}, nil)

			require.NoError(t, res.err)
			require.Equal(t, "ok", res.lines[0]["status"])

			records := resultRecordsOf(t, res.dir)
			require.Len(t, records, 1)
			require.Equal(t, tc.title, records[0]["title"])
			require.Equal(t, tc.want, records[0]["status"])
		})
	}
}
