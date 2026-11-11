package runner

import (
	"flag"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateInputConfig_MaxBlocked(t *testing.T) {
	t.Parallel()

	cfg := jsonlConfig()
	cfg.MaxBlocked = -1
	require.Equal(t, []string{"flag -max-blocked must be 0 or greater"}, inputLines(t, cfg))

	for _, n := range []int{0, 1, 100} {
		cfg = jsonlConfig()
		cfg.MaxBlocked = n
		require.NoError(t, ValidateInputConfig(cfg), n)
	}

	cfg = &Config{InputFile: "q.txt", ExplicitFlags: map[string]bool{"max-blocked": true}}
	require.Equal(t, []string{"flag -max-blocked requires -input-format jsonl"}, inputLines(t, cfg))
}

// The accepted exception of contract §1.3: a value the flag package cannot parse
// is rejected by the package itself.
func TestMaxBlockedFlagMalformedUsesFlagPackage(t *testing.T) {
	t.Parallel()

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(discard{})

	cfg := &Config{}
	registerAbortFlags(fs, cfg)

	err := fs.Parse([]string{"-max-blocked", "abc"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid value")
	require.Contains(t, err.Error(), "-max-blocked")

	require.NoError(t, fs.Parse([]string{"-max-blocked", "3"}))
	require.Equal(t, 3, cfg.MaxBlocked)
}
