package pipeline

import (
	"strings"
	"testing"
)

func TestSchemaAndVersionFields(t *testing.T) {
	base := "name: p\nentry: a\nagents:\n  - id: a\n    runtime: mock\n"
	if p, err := Parse([]byte("schema: 1\nversion: 1.2.0\n" + base)); err != nil || p.Version != "1.2.0" || p.Schema != 1 {
		t.Fatalf("valid = %+v %v", p, err)
	}
	if _, err := Parse([]byte("schema: 2\n" + base)); err == nil || !strings.Contains(err.Error(), "upgrade SignalBox") {
		t.Fatalf("future schema: %v", err)
	}
	if _, err := Parse([]byte("version: v1\n" + base)); err == nil || !strings.Contains(err.Error(), "semantic version") {
		t.Fatalf("bad version: %v", err)
	}
}

func TestCompareVersionsAndNextPatch(t *testing.T) {
	ordered := []string{"0.1.0", "0.1.1", "0.2.0-alpha", "0.2.0-alpha.2", "0.2.0-alpha.10", "0.2.0-beta", "0.2.0", "0.10.0", "1.0.0"}
	for i := 1; i < len(ordered); i++ {
		if CompareVersions(ordered[i-1], ordered[i]) != -1 || CompareVersions(ordered[i], ordered[i-1]) != 1 {
			t.Errorf("%s should sort before %s", ordered[i-1], ordered[i])
		}
	}
	if NextPatch("1.2.3") != "1.2.4" || NextPatch("1.2.3-rc.1") != "1.2.3" {
		t.Fatal("NextPatch")
	}
	for _, bad := range []string{"1.2", "01.2.3", "1.2.3-", "v1.2.3"} {
		if ValidVersion(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
