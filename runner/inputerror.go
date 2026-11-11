package runner

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// exitInvalidInput is the process exit code for invalid input (docs/contracts.md §1.3).
const exitInvalidInput = 2

// InputError carries every problem found in the input before any network
// activity. Each line is printed to stderr as is; the process exits with code 2.
type InputError struct {
	Lines []string
}

func (e *InputError) Error() string {
	return strings.Join(e.Lines, "\n")
}

// ReportInputError prints the lines of an *InputError (found anywhere in the
// error chain) and returns the exit code to use. ok is false for other errors.
func ReportInputError(w io.Writer, err error) (code int, ok bool) {
	var inputErr *InputError
	if err == nil || !errors.As(err, &inputErr) {
		return 0, false
	}

	for _, line := range inputErr.Lines {
		fmt.Fprintln(w, line)
	}

	return exitInvalidInput, true
}

// requiresJSONLFlags lists the flags that only make sense with
// -input-format jsonl; they are rejected when set explicitly without it.
var requiresJSONLFlags = []string{"maps-base-url", "viewport", "raw-dir", "search-report"}

// ValidateInputConfig checks the flag combinations around -input-format. It
// returns an *InputError listing every problem, or nil.
func ValidateInputConfig(cfg *Config) error {
	var lines []string

	switch cfg.InputFormat {
	case "", "lines", "jsonl":
	default:
		lines = append(lines, "flag -input-format must be lines or jsonl")
	}

	if cfg.InputFormat == "jsonl" {
		lines = append(lines, jsonlFlagConflicts(cfg)...)
	} else {
		for _, name := range requiresJSONLFlags {
			if cfg.ExplicitFlags[name] {
				lines = append(lines, "flag -"+name+" requires -input-format jsonl")
			}
		}
	}

	if len(lines) == 0 {
		return nil
	}

	return &InputError{Lines: lines}
}

func jsonlFlagConflicts(cfg *Config) []string {
	var lines []string

	conflict := func(name, reason string) {
		lines = append(lines, "flag -"+name+" cannot be used with -input-format jsonl ("+reason+")")
	}

	if cfg.GridBBox != "" {
		conflict("grid-bbox", "the task file sets the position")
	}

	if cfg.Resume {
		conflict("resume", "resume is not supported for task files")
	}

	if cfg.GeoCoordinates != "" {
		conflict("geo", "the task file sets the position")
	}

	if cfg.FastMode {
		conflict("fast-mode", "the task file sets the mode")
	}

	if cfg.InputFile == "" {
		lines = append(lines, "flag -input-format jsonl requires -input")
	}

	if cfg.Dsn != "" || cfg.WebRunner || cfg.AwsLamdbaRunner || cfg.AwsLambdaInvoker {
		lines = append(lines, "flag -input-format jsonl works only with -input and file output")
	}

	return lines
}

// explicitFlags returns the names of the flags set on the command line.
func explicitFlags(fs *flag.FlagSet) map[string]bool {
	set := map[string]bool{}

	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	return set
}

// hiddenFlags are valid flags that -h does not list.
var hiddenFlags = map[string]bool{"maps-base-url": true}

// usage prints the flag help without the hidden flags.
func usage() {
	fmt.Fprintf(flag.CommandLine.Output(), "Usage of %s:\n", os.Args[0])

	flag.VisitAll(func(f *flag.Flag) {
		if hiddenFlags[f.Name] {
			return
		}

		fmt.Fprintf(flag.CommandLine.Output(), "  -%s\n    \t%s (default %q)\n", f.Name, f.Usage, f.DefValue)
	})
}
