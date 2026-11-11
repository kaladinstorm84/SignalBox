package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
)

func TestPluginRuntimeExitMappingsAndCancellation(t *testing.T) {
	if _, err := exec.LookPath("python"); err != nil {
		t.Skip("Python is required for this integration test")
	}
	for _, exit := range []int{0, 1, 2, 3} {
		t.Run(fmt.Sprint(exit), func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "plugins", "example")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			for name, data := range map[string]string{
				"plugin.yaml": "language: python\nentry: main.py\ncodes: {success: 0, fail: 1, retry: 2}\n",
				"main.py":     fmt.Sprintf("import sys\nprint('feedback')\nsys.exit(%d)\n", exit),
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			p, err := pipeline.ParseWith([]byte("name: p\nagents:\n  - id: a\n    type: plugin:example\n"), pipeline.Options{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			rt, err := NewCode(&p.Nodes[0], root)
			if err != nil {
				t.Fatal(err)
			}
			_, err = rt.Invoke(context.Background(), Request{})
			var outcome *CodeOutcomeError
			switch exit {
			case 0:
				if err != nil {
					t.Fatal(err)
				}
			case 1, 2:
				expected := map[int]string{1: "fail", 2: "retry"}[exit]
				if !errors.As(err, &outcome) || outcome.Outcome != expected || outcome.Feedback != "feedback" {
					t.Fatalf("exit mapping: %v", err)
				}
			default:
				if err == nil || errors.As(err, &outcome) {
					t.Fatalf("unmapped exit: %v", err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			start := time.Now()
			if _, err := rt.Invoke(ctx, Request{}); err == nil {
				t.Fatal("ignored cancellation")
			}
			if time.Since(start) > 5*time.Second {
				t.Fatal("cancellation took too long")
			}
		})
	}
}
