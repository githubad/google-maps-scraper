package gmaps

import (
	"bufio"
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

const contractsPath = "../../docs/contracts.md"

var contractHeader = regexp.MustCompile(`^# GMX contracts: version (\d+\.\d+\.\d+)\s*$`)

// TestContractVersionMatchesDocs keeps the engine's ContractVersion equal to the
// version in the header of docs/contracts.md (parent repo). It is skipped when
// the file is not there, for example when the engine is checked out alone.
func TestContractVersionMatchesDocs(t *testing.T) {
	t.Parallel()

	f, err := os.Open(contractsPath)
	if err != nil {
		t.Skipf("docs/contracts.md not found at %s (%v); run the engine tests from the parent repo checkout or via scripts/engine.sh", contractsPath, err)
	}

	defer f.Close()

	sc := bufio.NewScanner(f)
	require.True(t, sc.Scan(), "contracts.md is empty")

	m := contractHeader.FindStringSubmatch(sc.Text())
	require.NotNil(t, m, "unexpected first line of contracts.md: %q", sc.Text())
	require.Equal(t, m[1], ContractVersion, "bump gmaps.ContractVersion together with docs/contracts.md")
}

func TestReportCarriesContractVersion(t *testing.T) {
	t.Parallel()

	env := newTrackerEnv(t)
	env.tr.Register("a", ModeList, intPtr(6))
	env.tr.PageDone("a", okPage(0, 5, 5))
	env.tr.Finish("a")

	require.Equal(t, ContractVersion, env.lines(t)[0]["contract"])
}
