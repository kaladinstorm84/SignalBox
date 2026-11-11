package registry

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

func TestPluginPublishInstallAndExecute(t *testing.T) {
	cfg := project(t, "version: 1\nruntimes:\n  mock: {type: mock}\n", map[string]string{
		"plugins/example/plugin.yaml": "description: Reusable script\nlanguage: python\nentry: main.py\n",
		"plugins/example/main.py":     "import sys\nfrom helper import transform\nprint(transform(sys.stdin.read()))\n",
		"plugins/example/helper.py":   "def transform(text):\n    return 'processed: ' + text\n",
		"plugins/example/asset.bin":   "\x00\x01\xff",
		"pipelines/demo.yaml":         "name: demo\nagents:\n  - id: a\n    type: plugin:example\n",
	})
	r := newRegistry(t)
	m, err := r.PublishPlugin(cfg, "example", PublishOptions{By: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != KindPlugin || m.Version != "0.1.0" || len(m.Files) != 4 || m.Entry != "plugins/example/plugin.yaml" {
		t.Fatalf("manifest: %+v", m)
	}
	if _, err := r.Verify(m); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PublishPlugin(cfg, "example", PublishOptions{Version: m.Version}); err == nil {
		t.Fatal("overwrote immutable version")
	}
	consumer := project(t, "version: 1\nruntimes:\n  mock: {type: mock}\n", nil)
	if _, err := r.Install(consumer, "example", InstallOptions{Digest: "sha256:wrong"}); err == nil {
		t.Fatal("ignored digest pin")
	}
	result, err := r.Install(consumer, "example", InstallOptions{Digest: m.Digest})
	if err != nil || len(result.Written) != 4 {
		t.Fatalf("install: %+v %v", result, err)
	}
	lock, err := ReadLock(consumer.Root)
	if err != nil || lock.Packages["example"].Kind != KindPlugin {
		t.Fatalf("lock: %+v %v", lock, err)
	}
	bundled, err := r.Publish(cfg, "demo", PublishOptions{})
	if err != nil || len(bundled.Files) != 5 || len(bundled.Runtimes) != 0 {
		t.Fatalf("bundled plugin: %+v %v", bundled, err)
	}
	installed, err := r.Install(consumer, "demo", InstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p, err := pipeline.Load(filepath.Join(consumer.Root, filepath.FromSlash(installed.Manifest.Entry)), pipeline.Options{Root: consumer.Root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("python"); err != nil {
		t.Skip("Python is required for execution check")
	}
	rt, err := runtime.NewCode(&p.Nodes[0], consumer.Root)
	if err != nil {
		t.Fatal(err)
	}
	out, err := rt.Invoke(context.Background(), runtime.Request{Input: "hello plugin"})
	if err != nil || strings.TrimSpace(out) != "processed: hello plugin" {
		t.Fatalf("execution: %q %v", out, err)
	}
	if _, err := runtime.NewCode(&p.Nodes[0], consumer.Root); err != nil {
		t.Fatalf("execution changed plugin folder: %v", err)
	}
	writeFiles(t, consumer.Root, map[string]string{"plugins/example/helper.py": "changed"})
	if _, err := runtime.NewCode(&p.Nodes[0], consumer.Root); err == nil {
		t.Fatal("ran changed plugin snapshot")
	}
}

func TestPluginUpgradeRemovesOnlyTrackedObsoleteFiles(t *testing.T) {
	cfg := project(t, "version: 1\nruntimes:\n  mock: {type: mock}\n", map[string]string{
		"plugins/example/plugin.yaml":  "language: python\nentry: main.py\n",
		"plugins/example/main.py":      "print('ok')\n",
		"plugins/example/obsolete.txt": "old resource",
	})
	r := newRegistry(t)
	if _, err := r.PublishPlugin(cfg, "example", PublishOptions{}); err != nil {
		t.Fatal(err)
	}
	consumer := project(t, "version: 1\nruntimes:\n  mock: {type: mock}\n", nil)
	if _, err := r.Install(consumer, "example", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(cfg.Root, "plugins", "example", "obsolete.txt")); err != nil {
		t.Fatal(err)
	}
	m, err := r.PublishPlugin(cfg, "example", PublishOptions{})
	if err != nil || m.Version != "0.1.1" {
		t.Fatalf("next patch: %+v %v", m, err)
	}
	clean := project(t, "version: 1\nruntimes:\n  mock: {type: mock}\n", nil)
	if _, err := r.Install(clean, "example@0.1.0", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	shared, err := ReadLock(clean.Root)
	if err != nil {
		t.Fatal(err)
	}
	shared.Packages["another"] = shared.Packages["example"]
	if err := WriteLock(clean.Root, shared); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Install(clean, "example", InstallOptions{Force: true}); err == nil || !strings.Contains(err.Error(), "also tracked") {
		t.Fatalf("removed another package's file: %v", err)
	}
	delete(shared.Packages, "another")
	if err := WriteLock(clean.Root, shared); err != nil {
		t.Fatal(err)
	}
	cleanUpgrade, err := r.Install(clean, "example", InstallOptions{})
	if err != nil || len(cleanUpgrade.Removed) != 1 {
		t.Fatalf("unchanged obsolete file did not upgrade: %+v %v", cleanUpgrade, err)
	}
	writeFiles(t, consumer.Root, map[string]string{"plugins/example/local.txt": "local file"})
	if _, err := r.Install(consumer, "example", InstallOptions{Force: true}); err == nil || !strings.Contains(err.Error(), "untracked plugin file") {
		t.Fatalf("untracked file: %v", err)
	}
	if err := os.Remove(filepath.Join(consumer.Root, "plugins", "example", "local.txt")); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, consumer.Root, map[string]string{"plugins/example/obsolete.txt": "local edit"})
	if _, err := r.Install(consumer, "example", InstallOptions{}); err == nil || !strings.Contains(err.Error(), "local edits") {
		t.Fatalf("obsolete local edit: %v", err)
	}
	res, err := r.Install(consumer, "example", InstallOptions{Force: true})
	if err != nil || strings.Join(res.Removed, ",") != "plugins/example/obsolete.txt" {
		t.Fatalf("upgrade removal: %+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(consumer.Root, "plugins", "example", "obsolete.txt")); !os.IsNotExist(err) {
		t.Fatalf("obsolete file remains: %v", err)
	}
}

func TestPluginPackageBoundariesAndKinds(t *testing.T) {
	r := newRegistry(t)
	for _, contents := range []map[string][]byte{
		{"plugins/other/plugin.yaml": []byte("language: python\nentry: main.py\n"), "plugins/other/main.py": []byte("print('ok')")},
		{"plugins/example/plugin.yaml": []byte("language: python\nentry: ../escape.py\n"), "plugins/example/main.py": []byte("print('ok')")},
		{"plugins/example/plugin.yaml": []byte("language: python\nentry: main.py\n"), "plugins/example/main.py": []byte("print('ok')"), "scripts/extra.py": []byte("print('outside')")},
	} {
		m, err := r.publishContents(&Manifest{Kind: KindPlugin, Name: "example", Version: "1.0.0", Entry: "plugins/example/plugin.yaml"}, contents)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Verify(m); err == nil {
			t.Fatal("accepted invalid plugin package")
		}
		// Each malformed package is isolated; published versions remain immutable.
		r = newRegistry(t)
	}
	cfg := publisher(t)
	if _, err := r.Publish(cfg, "flow", PublishOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PublishPlugin(cfg, "flow", PublishOptions{}); err == nil {
		t.Fatal("changed pipeline package kind")
	}
}
