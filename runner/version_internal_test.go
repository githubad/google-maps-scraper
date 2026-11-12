package runner

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestEngineVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		main string
		want string
	}{
		{"v1.18.1", "v1.18.1-gmx.3"},
		{"", "v0.0.0-devel-gmx.3"},
		{"(devel)", "v0.0.0-devel-gmx.3"},
		{"v0.0.0-20261110120000-abcdef123456+dirty", "v0.0.0-20261110120000-abcdef123456+dirty-gmx.3"},
	}

	for _, tc := range tests {
		if got := engineVersion(tc.main); got != tc.want {
			t.Errorf("engineVersion(%q) = %q, want %q", tc.main, got, tc.want)
		}
	}

	if GMXVersion != "gmx.3" {
		t.Errorf("GMXVersion = %q, want gmx.3", GMXVersion)
	}
}

func TestPrintVersion(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	printVersion(&out, "v1.18.1")

	if out.String() != "v1.18.1-gmx.3\n" {
		t.Errorf("printVersion wrote %q", out.String())
	}
}

func TestDockerfileDisablesTelemetry(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}

	// checkouts on Windows may use CRLF line endings
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	stages := strings.Split(text, "\nFROM ")
	final := stages[len(stages)-1]

	if !strings.Contains(final, "\nENV DISABLE_TELEMETRY=1\n") {
		t.Error("the final stage of the Dockerfile must set ENV DISABLE_TELEMETRY=1")
	}
}

func TestEngineVersionFromBuildInfo(t *testing.T) {
	t.Parallel()

	if got := EngineVersion(); !strings.HasSuffix(got, "-"+GMXVersion) {
		t.Errorf("EngineVersion() = %q, want the -%s suffix", got, GMXVersion)
	}
}
