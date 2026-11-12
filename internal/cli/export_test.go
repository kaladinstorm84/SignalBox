package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportCommandWritesStandaloneScriptWithoutOverwriting(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Mkdir("pipelines", 0o700); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{
		"signalbox.yaml": "version: 1\nruntimes:\n  agent:\n    type: codex\n",
		filepath.Join("pipelines", "example.yaml"): "name: example\nagents:\n  - id: write\n    runtime: agent\n    prompt: Write an article\n",
	} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := cmdExport([]string{"example", "--format", "python", "--output", "example.py"}, &out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("example.py")
	if err != nil || !bytes.HasPrefix(data, []byte("#!/usr/bin/env python3")) || bytes.Contains(data, []byte("__SIGNALBOX_SPEC__")) {
		t.Fatalf("invalid Python export: %v", err)
	}
	if err := cmdExport([]string{"example", "--output", "example.py"}, &out); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing export was not protected: %v", err)
	}
	preserved, _ := os.ReadFile("example.py")
	if !bytes.Equal(data, preserved) {
		t.Fatal("existing export changed")
	}
	if err := cmdExport([]string{"--output", "example.py", "--force", "example"}, &out); err != nil {
		t.Fatalf("explicit overwrite: %v", err)
	}
	if err := cmdExport([]string{"example", "--format", "javascript", "--output", "other.js"}, &out); err == nil {
		t.Fatal("unsupported format accepted")
	}
}
