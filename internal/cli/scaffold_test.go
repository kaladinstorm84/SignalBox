package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCreatesProjectGuideAndPreservesIt(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit([]string{dir}); err != nil {
		t.Fatal(err)
	}

	guidePath := filepath.Join(dir, "AGENTS.md")
	content, err := os.ReadFile(guidePath)
	if err != nil {
		t.Fatalf("read generated AGENTS.md: %v", err)
	}
	if !strings.Contains(string(content), "# SignalBox Project Guide") || !strings.Contains(string(content), "signalbox validate") {
		t.Fatalf("generated AGENTS.md is missing project guidance:\n%s", content)
	}

	const userContent = "# My project-specific instructions\n"
	if err := os.WriteFile(guidePath, []byte(userContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdInit([]string{dir}); err != nil {
		t.Fatal(err)
	}
	content, err = os.ReadFile(guidePath)
	if err != nil {
		t.Fatalf("read preserved AGENTS.md: %v", err)
	}
	if string(content) != userContent {
		t.Fatalf("init overwrote existing AGENTS.md: %q", content)
	}
}