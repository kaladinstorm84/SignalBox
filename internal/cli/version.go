package cli

import "runtime/debug"

func init() {
	info, _ := debug.ReadBuildInfo()
	Version = buildVersion(Version, info)
}

func buildVersion(stamped string, info *debug.BuildInfo) string {
	if stamped != "" && stamped != "dev" {
		return stamped
	}
	if info != nil && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
