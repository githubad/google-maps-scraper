package runner

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// F003 flags that need -input-format jsonl (FR-013, AS-15).
func TestValidateInputConfig_SearchReportRequiresJSONL(t *testing.T) {
	t.Parallel()

	cfg := &Config{InputFile: "q.txt", ExplicitFlags: map[string]bool{"search-report": true}}
	require.Equal(t, []string{"flag -search-report requires -input-format jsonl"}, inputLines(t, cfg))

	cfg = jsonlConfig()
	cfg.SearchReport = "searches.jsonl"
	cfg.ExplicitFlags = map[string]bool{"search-report": true}
	require.NoError(t, ValidateInputConfig(cfg))

	cfg = &Config{InputFile: "q.txt", ExplicitFlags: map[string]bool{"search-report": true, "viewport": true}}
	require.Equal(t, []string{
		"flag -viewport requires -input-format jsonl",
		"flag -search-report requires -input-format jsonl",
	}, inputLines(t, cfg), "listed with the other flags of its kind")
}
