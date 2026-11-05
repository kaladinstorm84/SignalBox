package cli

import "testing"

func TestParamFlag(t *testing.T) {
	p := paramFlag{}
	if err := p.Set("topic=a=b"); err != nil || p["topic"] != "a=b" {
		t.Fatalf("set: %v %v", err, p)
	}
	if err := p.Set("empty="); err != nil || p["empty"] != "" {
		t.Fatalf("empty: %v", err)
	}
	for _, bad := range []string{"novalue", "=x", "topic=again"} {
		if err := p.Set(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}
