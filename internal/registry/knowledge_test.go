package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/knowledgebase"
)

func TestKnowledgePublishInstallUpgradeAndIntegrity(t *testing.T) {
	r := newRegistry(t)
	pub := project(t, "version: 1\nruntimes:\n  mock: {type: mock}\n", map[string]string{
		"knowledge-bases/handbook/guide.md": "Original guide",
		"knowledge-bases/handbook/faq.txt":  "Questions and answers",
	})
	m, err := r.PublishKnowledge(pub, "handbook", "Product reference", PublishOptions{By: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != KindKnowledge || m.Version != "0.1.0" || len(m.Files) != 2 || m.Description != "Product reference" {
		t.Fatalf("manifest = %+v", m)
	}
	publishable, err := PublishableKnowledge(pub)
	if err != nil || strings.Join(publishable, ",") != "handbook" {
		t.Fatalf("publishable = %v, %v", publishable, err)
	}
	list, err := r.Search("product")
	if err != nil || len(list) != 1 || list[0].Kind != KindKnowledge {
		t.Fatalf("search = %+v, %v", list, err)
	}
	if _, err := r.PublishKnowledge(pub, "handbook", "", PublishOptions{Version: m.Version}); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("republish = %v", err)
	}
	consumer := project(t, "version: 1\nruntimes:\n  mock: {type: mock}\n", nil)
	if _, err := r.Install(consumer, "handbook", InstallOptions{Digest: "sha256:wrong"}); err == nil {
		t.Fatal("accepted incorrect digest")
	}
	result, err := r.Install(consumer, "handbook", InstallOptions{Digest: m.Digest})
	if err != nil || len(result.Written) != 2 {
		t.Fatalf("install = %+v, %v", result, err)
	}
	text, err := knowledgebase.New(consumer.Root).Content([]string{"handbook"})
	if err != nil || !strings.Contains(text, "Original guide") {
		t.Fatalf("installed context = %q, %v", text, err)
	}
	lock, err := ReadLock(consumer.Root)
	if err != nil || lock.Packages["handbook"].Kind != KindKnowledge {
		t.Fatalf("lock = %+v, %v", lock, err)
	}
	writeFiles(t, pub.Root, map[string]string{"knowledge-bases/handbook/guide.md": "Updated guide"})
	second, err := r.PublishKnowledge(pub, "handbook", "Updated reference", PublishOptions{})
	if err != nil || second.Version != "0.1.1" {
		t.Fatalf("publish upgrade = %+v, %v", second, err)
	}
	result, err = r.Install(consumer, "handbook", InstallOptions{})
	if err != nil || len(result.Replaced) != 1 {
		t.Fatalf("upgrade = %+v, %v", result, err)
	}
	writeFiles(t, consumer.Root, map[string]string{"knowledge-bases/handbook/guide.md": "Local edit"})
	if _, err := r.Install(consumer, "handbook@0.1.0", InstallOptions{}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("local edit protection = %v", err)
	}
	if _, err := r.Install(consumer, "handbook@0.1.0", InstallOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, r.Dir, map[string]string{"handbook/0.1.1/files/knowledge-bases/handbook/guide.md": "Tampered"})
	if _, err := r.Install(consumer, "handbook", InstallOptions{Force: true}); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("tamper detection = %v", err)
	}
}

func TestKnowledgePackageValidationAndKindClashes(t *testing.T) {
	r := newRegistry(t)
	cfg := publisher(t)
	writeFiles(t, cfg.Root, map[string]string{
		"knowledge-bases/flow/guide.md": "Reference",
		"knowledge-bases/docs/guide.md": "Reference",
	})
	if _, err := r.Publish(cfg, "flow", PublishOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PublishKnowledge(cfg, "flow", "", PublishOptions{}); err == nil || !strings.Contains(err.Error(), "pipeline") {
		t.Fatalf("pipeline name clash = %v", err)
	}
	if _, err := r.PublishKnowledge(cfg, "docs", "", PublishOptions{}); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, cfg.Root, map[string]string{"pipelines/docs.yaml": "name: docs\nagents:\n  - id: a\n    runtime: mock\n"})
	if _, err := r.Publish(cfg, "docs", PublishOptions{}); err == nil || !strings.Contains(err.Error(), "knowledge base") {
		t.Fatalf("knowledge name clash = %v", err)
	}
	for _, rel := range []string{"pipelines/escape.yaml", "knowledge-bases/other/guide.md", "knowledge-bases/docs/image.png"} {
		m := &Manifest{Kind: KindKnowledge, Name: "docs", Version: "1.0.0", Entry: rel,
			Files: []File{{Path: rel, SHA256: hashBytes([]byte("text")), Size: 4}}}
		m.Digest = Digest(m.Files)
		writeFiles(t, r.Dir, map[string]string{"docs/1.0.0/files/" + rel: "text"})
		if _, err := r.Verify(m); err == nil {
			t.Errorf("accepted invalid knowledge package file %q", rel)
		}
	}
	if err := os.MkdirAll(filepath.Join(cfg.Root, "knowledge-bases", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PublishKnowledge(cfg, "empty", "", PublishOptions{}); err == nil {
		t.Fatal("published empty knowledge base")
	}
}

func TestPipelinePublicationBundlesKnowledge(t *testing.T) {
	r := newRegistry(t)
	cfg := project(t, "runtimes:\n  mock: {type: mock}\n", map[string]string{
		"pipelines/answer.yaml":             "name: answer\nagents:\n  - id: a\n    runtime: mock\n    knowledge_bases: [handbook]\n",
		"knowledge-bases/handbook/guide.md": "Shared reference",
	})
	m, err := r.Publish(cfg, "answer", PublishOptions{})
	if err != nil || len(m.Files) != 2 {
		t.Fatalf("publish = %+v, %v", m, err)
	}
	consumer := project(t, "pipelines_dir: flows\nruntimes:\n  mock: {type: mock}\n", nil)
	if _, err := r.Install(consumer, "answer", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(consumer.Root, "knowledge-bases", "handbook", "guide.md")); err != nil || string(data) != "Shared reference" {
		t.Fatalf("bundled knowledge = %q, %v", data, err)
	}
	m.Kind = ""
	if m.PackageKind() != KindPipeline {
		t.Fatal("legacy package kind changed")
	}
	if _, err := r.Verify(m); err != nil {
		t.Fatalf("legacy manifest verification = %v", err)
	}
}
