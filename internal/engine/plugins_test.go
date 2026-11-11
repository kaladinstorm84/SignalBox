package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

func TestPluginNodeRetriesAndHandsOffWithCustomType(t *testing.T) {
	if _, err := exec.LookPath("python"); err != nil {
		t.Skip("Python is required for this integration test")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "plugins", "example")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"plugin.yaml": "language: python\nentry: main.py\ntimeout_seconds: 10\n",
		"main.py":     "import sys\nfrom pathlib import Path\nfrom helper import transform\nstate = Path('plugin-attempt.txt')\nif not state.exists():\n    state.write_text('retried')\n    sys.exit(1)\nprint(transform(sys.stdin.read()))\n",
		"helper.py":   "def transform(text):\n    return 'plugin output: ' + text\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := pipeline.ParseWith([]byte("name: p\nagents:\n  - id: a\n    type: plugin:example\n    retry: {count: 1}\n    handoff: [b]\n  - id: b\n    runtime: mock\n"), pipeline.Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	eng, err := NewWithLimit(map[string]*pipeline.Pipeline{"p": p}, reg, nil, 1, root)
	if err != nil {
		t.Fatal(err)
	}
	id, err := eng.Start("p", "handoff payload")
	if err != nil {
		t.Fatal(err)
	}
	execution := waitDone(t, eng, id)
	if execution.Status != StatusCompleted {
		t.Fatalf("execution: %+v", execution)
	}
	var found bool
	for _, run := range execution.Runs {
		if run.NodeID != "a" {
			continue
		}
		found = true
		if run.Type != "plugin:example" || !strings.Contains(run.Output, "plugin output: handoff payload") {
			t.Fatalf("plugin run: %+v", run)
		}
	}
	if !found || !restartableType("plugin:example") || !contextSourceType("plugin:example") {
		t.Fatal("plugin type lost code-node capabilities")
	}
}
