package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryCommands(t *testing.T) {
	registryDir := t.TempDir()
	publisher := t.TempDir()
	consumer := t.TempDir()
	write := func(root, rel, content string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := "version: 1\nruntimes:\n  mock: {type: mock}\nregistry:\n  path: " + filepath.ToSlash(registryDir) + "\n"
	write(publisher, "signalbox.yaml", cfg)
	write(publisher, "pipelines/notes.yaml", "name: notes\ndescription: Release notes\nentry: a\nagents:\n  - id: a\n    runtime: mock\n")
	write(publisher, "knowledge-bases/handbook/guide.md", "Shared reference text")
	write(consumer, "signalbox.yaml", "version: 1\nruntimes:\n  mock: {type: mock}\n")

	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := cmdRegistry(args, &out); err != nil {
			t.Fatalf("registry %v: %v", args, err)
		}
		return out.String()
	}

	t.Chdir(publisher)
	if out := run("publish", "notes"); !strings.Contains(out, "published notes@0.1.0") {
		t.Fatalf("publish: %s", out)
	}
	if out := run("publish", "notes", "--version", "1.0.0"); !strings.Contains(out, "notes@1.0.0") {
		t.Fatalf("publish version: %s", out)
	}
	if out := run("search", "release"); !strings.Contains(out, "notes") || !strings.Contains(out, "1.0.0") {
		t.Fatalf("search: %s", out)
	}
	if out := run("versions", "notes"); strings.Index(out, "1.0.0") > strings.Index(out, "0.1.0") {
		t.Fatalf("versions not newest first: %s", out)
	}
	if out := run("show", "notes@0.1.0"); !strings.Contains(out, "pipelines/notes.yaml") || !strings.Contains(out, "Digest:") {
		t.Fatalf("show: %s", out)
	}
	if out := run("show", "notes", "--file", "pipelines/notes.yaml"); !strings.Contains(out, "Release notes") {
		t.Fatalf("show --file: %s", out)
	}
	if out := run("publish-knowledge", "handbook", "--description", "Shared handbook"); !strings.Contains(out, "published handbook@0.1.0") {
		t.Fatalf("publish knowledge: %s", out)
	}
	if out := run("search", "handbook"); !strings.Contains(out, "knowledge") {
		t.Fatalf("search knowledge: %s", out)
	}
	if out := run("show", "handbook"); !strings.Contains(out, "Kind: knowledge") || !strings.Contains(out, "guide.md") {
		t.Fatalf("show knowledge: %s", out)
	}

	// The consumer has no registry configured, so it passes --registry.
	t.Chdir(consumer)
	var out bytes.Buffer
	if err := cmdRegistry([]string{"search"}, &out); err == nil || !strings.Contains(err.Error(), "no registry configured") {
		t.Fatalf("unconfigured registry: %v", err)
	}
	if out := run("install", "notes@0.1.0", "--registry", registryDir); !strings.Contains(out, "added     pipelines/notes.yaml") {
		t.Fatalf("install: %s", out)
	}
	if out := run("installed"); !strings.Contains(out, "notes") || !strings.Contains(out, "0.1.0") {
		t.Fatalf("installed: %s", out)
	}
	if out := run("install", "notes", "--registry", registryDir); !strings.Contains(out, "notes@1.0.0") {
		t.Fatalf("upgrade: %s", out)
	}
	if out := run("install", "handbook", "--registry", registryDir); !strings.Contains(out, "knowledge-bases/handbook/guide.md") {
		t.Fatalf("install knowledge: %s", out)
	}
	if data, err := os.ReadFile(filepath.Join(consumer, "knowledge-bases", "handbook", "guide.md")); err != nil || string(data) != "Shared reference text" {
		t.Fatalf("installed knowledge = %q, %v", data, err)
	}
}
