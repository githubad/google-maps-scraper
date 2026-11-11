package runner

import (
	"flag"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// AS-16, delay part.
func TestValidateInputConfig_DelayFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		min, max time.Duration
		want     []string
	}{
		{"valid range", time.Second, 5 * time.Second, nil},
		{"zero is fine", 0, 0, nil},
		{"max exactly 120s", 0, 120 * time.Second, nil},
		{"min above max", 5 * time.Second, time.Second, []string{"flag -delay-min must not be greater than -delay-max"}},
		{"max above 120s", 0, 121 * time.Second, []string{"flag -delay-max must be between 0s and 120s"}},
		{"min negative", -time.Second, 0, []string{"flag -delay-min must be between 0s and 120s"}},
		{"both out of range", -time.Second, 200 * time.Second, []string{
			"flag -delay-min must be between 0s and 120s",
			"flag -delay-max must be between 0s and 120s",
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := jsonlConfig()
			cfg.DelayMin, cfg.DelayMax = tc.min, tc.max

			require.Equal(t, tc.want, inputLines(t, cfg))
		})
	}
}

// AS-15 for the delay flags.
func TestValidateInputConfig_DelayFlagsRequireJSONL(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"delay-min", "delay-max"} {
		cfg := &Config{InputFile: "q.txt", ExplicitFlags: map[string]bool{name: true}}
		require.Equal(t, []string{"flag -" + name + " requires -input-format jsonl"}, inputLines(t, cfg), name)
	}
}

// The accepted exception of contract §1.3: a value the flag package cannot parse
// is rejected by the package itself.
func TestDelayFlagMalformedUsesFlagPackage(t *testing.T) {
	t.Parallel()

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(discard{})

	cfg := &Config{}
	registerPacingFlags(fs, cfg)

	err := fs.Parse([]string{"-delay-min", "abc"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid value")
	require.Contains(t, err.Error(), "-delay-min")

	require.NoError(t, fs.Parse([]string{"-delay-min", "2s", "-delay-max", "5s"}))
	require.Equal(t, 2*time.Second, cfg.DelayMin)
	require.Equal(t, 5*time.Second, cfg.DelayMax)
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
