package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/registry"
)

func TestLocalListsExposeRegistrySources(t *testing.T) {
	handler, _, root := registryProject(t, t.TempDir(), map[string]string{
		"pipelines/imported.yaml":           "name: imported\nagents:\n  - id: a\n    runtime: mock\n",
		"pipelines/local.yaml":              "name: local\nagents:\n  - id: a\n    runtime: mock\n",
		"knowledge-bases/handbook/guide.md": "Imported reference",
		"knowledge-bases/bundled/guide.md":  "Bundled reference",
		"knowledge-bases/local/guide.md":    "Local reference",
	})
	lock := &registry.Lock{Packages: map[string]registry.LockEntry{
		"imported": {Version: "1.2.3", Files: []registry.File{{Path: "pipelines/imported.yaml"}, {Path: "knowledge-bases/bundled/guide.md"}, {Path: "knowledge-bases/bundled/faq.txt"}}},
		"handbook": {Kind: registry.KindKnowledge, Version: "2.0.0", Files: []registry.File{{Path: "knowledge-bases/handbook/guide.md"}}},
		"local":    {Kind: registry.KindKnowledge, Version: "3.0.0", Files: []registry.File{{Path: "knowledge-bases/local/guide.md"}}},
	}}
	if err := registry.WriteLock(root, lock); err != nil {
		t.Fatal(err)
	}
	read := func(path string) map[string][]registrySource {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", path, rec.Code, rec.Body.String())
		}
		var items []struct {
			Name    string           `json:"name"`
			Sources []registrySource `json:"registry_sources"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
			t.Fatal(err)
		}
		result := map[string][]registrySource{}
		for _, item := range items {
			result[item.Name] = item.Sources
		}
		return result
	}
	pipelines := read("/api/pipelines")
	if got := pipelines["imported"]; len(got) != 1 || got[0].Version != "1.2.3" || got[0].Kind != registry.KindPipeline {
		t.Fatalf("legacy pipeline provenance = %+v", got)
	}
	if len(pipelines["local"]) != 0 {
		t.Fatalf("local pipeline inherited knowledge package provenance: %+v", pipelines["local"])
	}
	bases := read("/api/knowledge-bases")
	if got := bases["handbook"]; len(got) != 1 || got[0].Version != "2.0.0" || got[0].Package != "handbook" {
		t.Fatalf("knowledge provenance = %+v", got)
	}
	if got := bases["bundled"]; len(got) != 1 || got[0].Package != "imported" || got[0].Version != "1.2.3" {
		t.Fatalf("bundled knowledge provenance = %+v", got)
	}
	entry := lock.Packages["handbook"]
	entry.Version = "2.1.0"
	lock.Packages["handbook"] = entry
	if err := registry.WriteLock(root, lock); err != nil {
		t.Fatal(err)
	}
	if got := read("/api/knowledge-bases")["handbook"]; len(got) != 1 || got[0].Version != "2.1.0" {
		t.Fatalf("upgraded provenance = %+v", got)
	}
	if err := os.Remove(filepath.Join(root, registry.LockFile)); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/pipelines", "/api/knowledge-bases"} {
		for name, sources := range read(path) {
			if len(sources) != 0 {
				t.Fatalf("missing lock: %s has provenance %+v", name, sources)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(root, registry.LockFile), []byte("{invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/pipelines", "/api/knowledge-bases"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("invalid lock: %s = %d", path, rec.Code)
		}
	}
}
