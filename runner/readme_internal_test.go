package runner

import (
	"os"
	"strings"
	"testing"
)

// The README section "GMX fork additions" must describe the F003 flags, the
// exit codes and the contract.
func TestReadmeDocumentsSearchReportFlags(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../README.md")
	if err != nil {
		t.Skipf("README.md not found: %v", err)
	}

	readme := string(raw)

	_, section, found := strings.Cut(readme, "## GMX fork additions")
	if !found {
		t.Fatal("README.md has no \"GMX fork additions\" section")
	}

	section, _, _ = strings.Cut(section, "\n## ")
	section = strings.ToLower(section)

	for _, want := range []string{
		"-search-report", "-delay-min", "-delay-max", "-max-blocked",
		"exit code", "docs/contracts.md", "rank",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("the GMX fork additions section does not mention %q", want)
		}
	}
}
