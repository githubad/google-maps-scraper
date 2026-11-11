package runner

import (
	"errors"
	"flag"
	"testing"

	"github.com/stretchr/testify/require"
)

func inputLines(t *testing.T, cfg *Config) []string {
	t.Helper()

	err := ValidateInputConfig(cfg)
	if err == nil {
		return nil
	}

	var ie *InputError

	require.True(t, errors.As(err, &ie), "ValidateInputConfig must return *InputError, got %T", err)

	return ie.Lines
}

func jsonlConfig() *Config {
	return &Config{InputFormat: "jsonl", InputFile: "tasks.jsonl", RunMode: RunModeFile}
}

func TestValidateInputConfig(t *testing.T) {
	t.Run("jsonl conflicts, one line each in a fixed order", func(t *testing.T) {
		cfg := jsonlConfig()
		cfg.GridBBox = "1,2,3,4"
		require.Equal(t, []string{
			"flag -grid-bbox cannot be used with -input-format jsonl (the task file sets the position)",
		}, inputLines(t, cfg))

		cfg = jsonlConfig()
		cfg.Resume = true
		require.Equal(t, []string{
			"flag -resume cannot be used with -input-format jsonl (resume is not supported for task files)",
		}, inputLines(t, cfg))

		cfg = jsonlConfig()
		cfg.GeoCoordinates = "30.1,-97.1"
		require.Equal(t, []string{
			"flag -geo cannot be used with -input-format jsonl (the task file sets the position)",
		}, inputLines(t, cfg))

		cfg = jsonlConfig()
		cfg.FastMode = true
		require.Equal(t, []string{
			"flag -fast-mode cannot be used with -input-format jsonl (the task file sets the mode)",
		}, inputLines(t, cfg))

		cfg = jsonlConfig()
		cfg.FastMode, cfg.GeoCoordinates, cfg.Resume, cfg.GridBBox = true, "1,2", true, "1,2,3,4"
		require.Equal(t, []string{
			"flag -grid-bbox cannot be used with -input-format jsonl (the task file sets the position)",
			"flag -resume cannot be used with -input-format jsonl (resume is not supported for task files)",
			"flag -geo cannot be used with -input-format jsonl (the task file sets the position)",
			"flag -fast-mode cannot be used with -input-format jsonl (the task file sets the mode)",
		}, inputLines(t, cfg))
	})

	t.Run("invalid input-format value", func(t *testing.T) {
		require.Equal(t, []string{"flag -input-format must be lines or jsonl"},
			inputLines(t, &Config{InputFormat: "xml", InputFile: "x"}))
	})

	t.Run("jsonl needs -input and file output", func(t *testing.T) {
		require.Equal(t, []string{"flag -input-format jsonl requires -input"},
			inputLines(t, &Config{InputFormat: "jsonl"}))

		for _, mutate := range []func(*Config){
			func(c *Config) { c.Dsn = "postgres://x" },
			func(c *Config) { c.WebRunner = true },
			func(c *Config) { c.AwsLamdbaRunner = true },
			func(c *Config) { c.AwsLambdaInvoker = true },
		} {
			cfg := jsonlConfig()
			mutate(cfg)
			require.Equal(t, []string{"flag -input-format jsonl works only with -input and file output"}, inputLines(t, cfg))
		}
	})

	t.Run("lines mode and unset format accept the same flags", func(t *testing.T) {
		for _, format := range []string{"", "lines"} {
			cfg := &Config{
				InputFormat: format, InputFile: "q.txt", GridBBox: "1,2,3,4", Resume: true,
				GeoCoordinates: "1,2", FastMode: true,
			}
			require.Nil(t, inputLines(t, cfg), format)
			require.NoError(t, ValidateInputConfig(cfg))
		}
	})

	t.Run("valid jsonl config", func(t *testing.T) {
		require.NoError(t, ValidateInputConfig(jsonlConfig()))
	})
}

func TestValidateInputConfig_RequiresJSONL(t *testing.T) {
	// The mechanism is tested with a temporary list; each commit adds its flag to
	// requiresJSONLFlags and TestRequiresJSONLFlagList pins the final list.
	orig := requiresJSONLFlags
	requiresJSONLFlags = []string{"viewport", "raw-dir"}

	t.Cleanup(func() { requiresJSONLFlags = orig })

	t.Run("explicit flag without jsonl is rejected", func(t *testing.T) {
		cfg := &Config{InputFile: "q.txt", ExplicitFlags: map[string]bool{"viewport": true}}
		require.Equal(t, []string{"flag -viewport requires -input-format jsonl"}, inputLines(t, cfg))

		cfg = &Config{InputFormat: "lines", InputFile: "q.txt", ExplicitFlags: map[string]bool{"raw-dir": true, "viewport": true}}
		require.Equal(t, []string{
			"flag -viewport requires -input-format jsonl",
			"flag -raw-dir requires -input-format jsonl",
		}, inputLines(t, cfg))
	})

	t.Run("accepted with jsonl", func(t *testing.T) {
		cfg := jsonlConfig()
		cfg.ExplicitFlags = map[string]bool{"viewport": true, "raw-dir": true}
		require.NoError(t, ValidateInputConfig(cfg))
	})

	t.Run("not explicit means not rejected", func(t *testing.T) {
		require.NoError(t, ValidateInputConfig(&Config{InputFile: "q.txt", ExplicitFlags: map[string]bool{}}))
		require.NoError(t, ValidateInputConfig(&Config{InputFile: "q.txt"}))
	})
}

func TestExplicitFlags(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.String("a", "x", "")
	fs.String("b", "y", "")
	fs.Bool("c", false, "")

	require.NoError(t, fs.Parse([]string{"-a", "x", "-c"}))
	require.Equal(t, map[string]bool{"a": true, "c": true}, explicitFlags(fs))
}
