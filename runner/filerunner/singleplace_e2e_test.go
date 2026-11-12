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

// A name search that Google answers with the single-place layout (one item instead
// of a header plus a list) is one result row: status ok, rows_seen 1, results 1,
// rank 1. Before F006 it was reported as "empty".
func TestSinglePlaceE2E_OneRow(t *testing.T) {
	body := readFixture(t, "single_01_hooters-round-rock.json")

	res := runBatch(t, []string{onePageTask()}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}, nil)

	require.NoError(t, res.err)
	require.EqualValues(t, 1, res.hits, "one page, no next page")
	require.Len(t, res.lines, 1)

	line := res.lines[0]
	require.Equal(t, "ok", line["status"])
	require.Nil(t, line["block_reason"])
	require.EqualValues(t, 1, line["rows_seen"])
	require.EqualValues(t, 1, line["results"])
	require.EqualValues(t, 1, line["pages_ok"])
	require.Equal(t, []int{200}, intsOf(t, line["http_statuses"]))

	raw, err := os.ReadFile(filepath.Join(res.dir, "results.jsonl"))
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

	require.Len(t, records, 1)
	require.Equal(t, "t_1", records[0]["input_id"])
	require.Equal(t, "Hooters", records[0]["title"])
	require.EqualValues(t, 1, records[0]["rank"])
	require.EqualValues(t, 0, records[0]["page"])
}
