package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/config"
)

const flowYAML = `name: flow
description: Drafts and checks a release note
imports:
  - from: ../shared/lib.yaml
    agents: [helper]
entry: draft
agents:
  - id: draft
    runtime: mock
    handoff: [helper, check]
  - id: check
    type: code
    language: python
    code-source: scripts/check.py
`

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func project(t *testing.T, signalbox string, files map[string]string) *config.Config {
	t.Helper()
	root := t.TempDir()
	writeFiles(t, root, files)
	cfg, err := config.Parse([]byte(signalbox), root)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func publisher(t *testing.T) *config.Config {
	return project(t, "version: 1\nruntimes:\n  mock: {type: mock}\n", map[string]string{
		"pipelines/flow.yaml": flowYAML,
		"shared/lib.yaml":     "agents:\n  - id: helper\n    runtime: mock\n",
		"scripts/check.py":    "print('ok')\n",
		"pipelines/Bad.yaml":  "name: Bad Name\nentry: a\nagents:\n  - id: a\n    runtime: mock\n",
	})
}

func newRegistry(t *testing.T) *Registry {
	t.Helper()
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPublishPackagesImportsAndCodeAndVersions(t *testing.T) {
	r := newRegistry(t)
	cfg := publisher(t)
	m, err := r.Publish(cfg, "flow", PublishOptions{By: "matt"})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range m.Files {
		paths = append(paths, f.Path)
	}
	if m.Version != "0.1.0" || strings.Join(paths, ",") != "pipelines/flow.yaml,scripts/check.py,shared/lib.yaml" ||
		m.Entry != "pipelines/flow.yaml" || !strings.HasPrefix(m.Digest, "sha256:") || strings.Join(m.Runtimes, ",") != "mock" {
		t.Fatalf("manifest = %+v", m)
	}
	if m2, err := r.Publish(cfg, "flow", PublishOptions{}); err != nil || m2.Version != "0.1.1" {
		t.Fatalf("auto version = %v %v", m2, err)
	}
	if _, err := r.Publish(cfg, "flow", PublishOptions{Version: "0.1.1"}); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("republish: %v", err)
	}
	if _, err := r.Publish(cfg, "flow", PublishOptions{Version: "0.10.0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Publish(cfg, "flow", PublishOptions{Version: "1.0.0-rc.1"}); err != nil {
		t.Fatal(err)
	}
	versions, err := r.Versions("flow")
	if err != nil || strings.Join(versions, ",") != "1.0.0-rc.1,0.10.0,0.1.1,0.1.0" {
		t.Fatalf("versions = %v %v", versions, err)
	}
	if _, v, err := r.Resolve("flow"); err != nil || v != "1.0.0-rc.1" {
		t.Fatalf("latest = %s %v", v, err)
	}
	if _, _, err := r.Resolve("flow@9.9.9"); err == nil || !strings.Contains(err.Error(), "available") {
		t.Fatalf("missing version: %v", err)
	}
	results, err := r.Search("release NOTE")
	if err != nil || len(results) != 1 || results[0].Latest != "1.0.0-rc.1" {
		t.Fatalf("search = %+v %v", results, err)
	}
	if results, _ := r.Search("nothing"); len(results) != 0 {
		t.Fatalf("search matched %+v", results)
	}
	if _, err := r.Publish(cfg, "Bad Name", PublishOptions{}); err == nil || !strings.Contains(err.Error(), "lowercase") {
		t.Fatalf("bad name: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(r.Dir, "flow")); len(entries) != 4 {
		t.Fatalf("temporary publish directories left behind: %v", entries)
	}
}

func TestPackageNamesArePortableDirectoryNames(t *testing.T) {
	for name, ok := range map[string]bool{"flow": true, "a_b-1": true, "Flow": false, "a.b": false, "..": false, "con": false, "": false, "-x": false} {
		if ValidName(name) != ok {
			t.Errorf("ValidName(%q) = %v", name, !ok)
		}
	}
	r := newRegistry(t)
	if err := os.MkdirAll(filepath.Join(r.Dir, "FLOW"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Publish(publisher(t), "flow", PublishOptions{}); err == nil {
		t.Fatal("published over a case-variant directory")
	}
}

func TestInstallVerifiesMapsDirectoriesAndLocks(t *testing.T) {
	r := newRegistry(t)
	pub := publisher(t)
	first, err := r.Publish(pub, "flow", PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	consumer := project(t, "version: 1\npipelines_dir: flows\nruntimes:\n  mock: {type: mock}\n", map[string]string{
		"flows/other.yaml": "name: other\nentry: a\nagents:\n  - id: a\n    runtime: mock\n",
	})
	if _, err := r.Install(consumer, "flow", InstallOptions{Digest: "sha256:nope"}); err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("digest pin: %v", err)
	}
	result, err := r.Install(consumer, "flow@0.1.0", InstallOptions{Digest: first.Digest})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.Written, ",") != "flows/flow.yaml,scripts/check.py,shared/lib.yaml" {
		t.Fatalf("written = %v", result.Written)
	}
	lock, err := ReadLock(consumer.Root)
	if err != nil || lock.Packages["flow"].Version != "0.1.0" || lock.Packages["flow"].Digest != first.Digest {
		t.Fatalf("lock = %+v %v", lock, err)
	}
	if again, err := r.Install(consumer, "flow", InstallOptions{}); err != nil || len(again.Skipped) != 3 {
		t.Fatalf("reinstall = %+v %v", again, err)
	}

	// An unmodified installed file upgrades without --force.
	writeFiles(t, pub.Root, map[string]string{"shared/lib.yaml": "agents:\n  - id: helper\n    runtime: mock\n    prompt: v2\n"})
	if _, err := r.Publish(pub, "flow", PublishOptions{}); err != nil {
		t.Fatal(err)
	}
	upgrade, err := r.Install(consumer, "flow", InstallOptions{})
	if err != nil || strings.Join(upgrade.Replaced, ",") != "shared/lib.yaml" {
		t.Fatalf("upgrade = %+v %v", upgrade, err)
	}

	// A locally modified file blocks the install unless forced.
	writeFiles(t, consumer.Root, map[string]string{"shared/lib.yaml": "agents:\n  - id: helper\n    runtime: mock\n    prompt: mine\n"})
	if _, err := r.Install(consumer, "flow@0.1.0", InstallOptions{}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("modified file: %v", err)
	}
	if _, err := r.Install(consumer, "flow@0.1.0", InstallOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
}

func TestInstallRejectsTamperingInvalidPackagesAndClashes(t *testing.T) {
	r := newRegistry(t)
	m, err := r.Publish(publisher(t), "flow", PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	consumer := func(signalbox string, files map[string]string) *config.Config {
		return project(t, signalbox, files)
	}
	mockOnly := "version: 1\nruntimes:\n  mock: {type: mock}\n"
	pkg := filepath.Join(r.Dir, "flow", m.Version)
	rewriteManifest := func(edit func(*Manifest)) {
		t.Helper()
		data, _ := json.Marshal(m)
		var copy Manifest
		_ = json.Unmarshal(data, &copy)
		edit(&copy)
		data, _ = json.Marshal(&copy)
		if err := os.WriteFile(filepath.Join(pkg, ManifestFile), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := r.Install(consumer("version: 1\nruntimes:\n  other: {type: mock}\n", nil), "flow", InstallOptions{}); err == nil || !strings.Contains(err.Error(), `runtime "mock"`) {
		t.Fatalf("missing runtime: %v", err)
	}
	clash := consumer(mockOnly, map[string]string{"pipelines/mine.yaml": "name: flow\nentry: a\nagents:\n  - id: a\n    runtime: mock\n"})
	if _, err := r.Install(clash, "flow", InstallOptions{Force: true}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("name clash: %v", err)
	}

	script := filepath.Join(pkg, "files", "scripts", "check.py")
	if err := os.WriteFile(script, []byte("print('evil')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Install(consumer(mockOnly, nil), "flow", InstallOptions{}); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("tampered file: %v", err)
	}
	if err := os.WriteFile(script, []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rewriteManifest(func(c *Manifest) { c.Files[1].SHA256 = strings.Repeat("0", 64) })
	if _, err := r.Install(consumer(mockOnly, nil), "flow", InstallOptions{}); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("tampered manifest: %v", err)
	}
	rewriteManifest(func(c *Manifest) { c.SchemaVersion = 99 })
	if _, err := r.Install(consumer(mockOnly, nil), "flow", InstallOptions{}); err == nil || !strings.Contains(err.Error(), "upgrade SignalBox") {
		t.Fatalf("future schema: %v", err)
	}
	rewriteManifest(func(c *Manifest) {
		c.Files[0].Path = "../escape.yaml"
		c.Digest = Digest(c.Files)
	})
	if _, err := r.Install(consumer(mockOnly, nil), "flow", InstallOptions{}); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("path escape: %v", err)
	}

	// A consistently re-hashed but invalid definition is still refused.
	broken := []byte("name: flow\nentry: missing\nagents:\n  - id: a\n    runtime: mock\n")
	if err := os.WriteFile(filepath.Join(pkg, "files", "pipelines", "flow.yaml"), broken, 0o644); err != nil {
		t.Fatal(err)
	}
	rewriteManifest(func(c *Manifest) {
		c.Files[0].SHA256 = hashBytes(broken)
		c.Digest = Digest(c.Files)
	})
	target := consumer(mockOnly, nil)
	if _, err := r.Install(target, "flow", InstallOptions{}); err == nil || !strings.Contains(err.Error(), "not a valid pipeline") {
		t.Fatalf("invalid definition: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target.Root, "pipelines", "flow.yaml")); !os.IsNotExist(err) {
		t.Fatal("refused install wrote files")
	}
}
