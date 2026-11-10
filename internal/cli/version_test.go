package cli

import (
	"runtime/debug"
	"testing"
)

func TestBuildVersion(t *testing.T) {
	tests := []struct {
		name    string
		stamped string
		info    *debug.BuildInfo
		want    string
	}{
		{"tagged install", "dev", &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}, "v1.2.3"},
		{"prerelease install", "dev", &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3-rc.1"}}, "v1.2.3-rc.1"},
		{"commit install", "dev", &debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20261110120000-abcdef123456"}}, "v0.0.0-20261110120000-abcdef123456"},
		{"stamp takes precedence", "v2.0.0", &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}, "v2.0.0"},
		{"stamp without metadata", "v2.0.0", nil, "v2.0.0"},
		{"local build", "dev", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "dev"},
		{"empty module version", "dev", &debug.BuildInfo{}, "dev"},
		{"no metadata", "dev", nil, "dev"},
		{"empty stamp", "", &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}, "v1.2.3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildVersion(tt.stamped, tt.info); got != tt.want {
				t.Fatalf("buildVersion = %q, want %q", got, tt.want)
			}
		})
	}
}
