package runner

import (
	"fmt"
	"io"
	"runtime/debug"
)

// GMXVersion is the version of the GMX fork additions. -version prints it after
// the upstream version, and other packages (the search report) can use it.
var GMXVersion = "gmx.3"

// engineVersion combines the upstream version with GMXVersion, for example
// "v1.18.1-gmx.1". Local builds have no tagged version and show v0.0.0-devel.
func engineVersion(mainVersion string) string {
	if mainVersion == "" || mainVersion == "(devel)" {
		mainVersion = "v0.0.0-devel"
	}

	return mainVersion + "-" + GMXVersion
}

// EngineVersion is the version string of this build: "<upstream version>-gmx.<n>".
func EngineVersion() string {
	mainVersion := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		mainVersion = info.Main.Version
	}

	return engineVersion(mainVersion)
}

func printVersion(w io.Writer, mainVersion string) {
	fmt.Fprintln(w, engineVersion(mainVersion))
}
