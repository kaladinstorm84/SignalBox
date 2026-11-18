package cli

import (
	"runtime/debug"

	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

func init() {
	info, _ := debug.ReadBuildInfo()
	Version = buildVersion(Version, info)
	runtime.ClientVersion = Version
}

// buildVersion prefers release stamps over go install metadata; local source
// builds have no module version and retain the development label.
func buildVersion(stamped string, info *debug.BuildInfo) string {
	if stamped != "" && stamped != "dev" {
		return stamped
	}
	if info != nil && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
