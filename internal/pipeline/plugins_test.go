package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginNodeResolutionAndValidation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "plugins", "example")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for file, data := range map[string]string{
		"plugin.yaml": "language: python\nentry: main.py\ntimeout_seconds: 12\ncodes: {success: 0, fail: 1, retry: 2}\n",
		"main.py":     "print('ok')\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	yaml := "name: example\nagents:\n  - id: check\n    type: plugin:example\n"
	p, err := ParseWith([]byte(yaml), Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	n := &p.Nodes[0]
	if n.Type != "plugin:example" || n.Type.BaseType() != TypeCode || n.Plugin == nil || n.TimeoutSeconds != 12 || n.CodeSource != "plugins/example/main.py" {
		t.Fatalf("resolved node: %+v", n)
	}
	outputNode := *n
	outputNode.Output = &OutputSpec{Schema: "example"}
	if outputNode.InvalidPolicy() != OnInvalidFail {
		t.Fatal("plugin output must default to fail, not AI repair")
	}
	before, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "asset.txt"), []byte("new resource"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := ParseWith([]byte(yaml), Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(changed)
	if err != nil || string(before) == string(after) {
		t.Fatalf("plugin change missing from pipeline snapshot: %v", err)
	}
	for _, extra := range []string{
		"language: python", "code-source: main.py", "timeout_seconds: 10", "codes: {success: 0, fail: 1, retry: 2}",
		"runtime: mock", "prompt: forbidden", "retry: {attempts: [{runtime: mock}]}",
	} {
		if _, err := ParseWith([]byte(yaml+"    "+extra+"\n"), Options{Root: root}); err == nil {
			t.Errorf("accepted plugin override %s", extra)
		}
	}
	if _, err := Parse([]byte(yaml)); err == nil || !strings.Contains(err.Error(), "project root") {
		t.Fatalf("missing root: %v", err)
	}
	if _, err := ParseWith([]byte(strings.ReplaceAll(yaml, "plugin:example", "plugin:missing")), Options{Root: root}); err == nil {
		t.Fatal("accepted missing plugin")
	}
	if _, err := ParseWith([]byte(yaml+"    files: path\n    context: {strategy: none}\n    retry: {count: 2}\n"), Options{Root: root}); err != nil {
		t.Fatalf("code-like capabilities rejected: %v", err)
	}
}
