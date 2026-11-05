package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/engine"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

func registryProject(t *testing.T, registryDir string, files map[string]string) (http.Handler, *engine.Engine, string) {
	t.Helper()
	root := t.TempDir()
	files["signalbox.yaml"] = "version: 1\nruntimes:\n  mock: {type: mock}\nregistry:\n  path: " + filepath.ToSlash(registryDir) + "\n"
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	load := func() (map[string]*pipeline.Pipeline, error) {
		pipelines, errs := pipeline.LoadDir(cfg.PipelinesPath(), pipeline.Options{Root: root})
		if len(errs) > 0 {
			return nil, errs[0]
		}
		return pipelines, nil
	}
	pipelines, err := load()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := runtime.NewRegistry(cfg.Runtimes)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(pipelines, reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return New(eng, "test", load, nil, nil, root).Handler(), eng, root
}

func TestRegistryEndpointsPublishInspectAndInstall(t *testing.T) {
	registryDir := t.TempDir()
	pub, _, _ := registryProject(t, registryDir, map[string]string{
		"pipelines/notes.yaml": "name: notes\ndescription: Release notes\nversion: 2.0.0\nentry: a\nagents:\n  - id: a\n    runtime: mock\n",
	})
	call := func(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}

	var listing struct {
		Configured  bool     `json:"configured"`
		Publishable []string `json:"publishable"`
		Packages    []struct {
			Name   string `json:"name"`
			Latest string `json:"latest"`
		} `json:"packages"`
	}
	rec := call(pub, http.MethodGet, "/api/registry", "")
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &listing) != nil || !listing.Configured || len(listing.Packages) != 0 || strings.Join(listing.Publishable, ",") != "notes" {
		t.Fatalf("empty listing = %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(pub, http.MethodPost, "/api/registry/publish", `{"pipeline":"notes","by":"matt"}`); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"version": "2.0.0"`) && !strings.Contains(rec.Body.String(), `"version":"2.0.0"`) {
		t.Fatalf("publish = %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(pub, http.MethodPost, "/api/registry/publish", `{"pipeline":"notes"}`); rec.Code != http.StatusConflict {
		t.Fatalf("republish = %d %s", rec.Code, rec.Body.String())
	}
	rec = call(pub, http.MethodGet, "/api/registry?q=release", "")
	if json.Unmarshal(rec.Body.Bytes(), &listing) != nil || len(listing.Packages) != 1 || listing.Packages[0].Latest != "2.0.0" {
		t.Fatalf("search = %s", rec.Body.String())
	}
	var detail struct {
		Manifest struct {
			Digest string `json:"digest"`
		} `json:"manifest"`
		Files       map[string]string `json:"files"`
		VerifyError string            `json:"verify_error"`
	}
	rec = call(pub, http.MethodGet, "/api/registry/notes/latest", "")
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &detail) != nil || detail.VerifyError != "" || !strings.Contains(detail.Files["pipelines/notes.yaml"], "Release notes") {
		t.Fatalf("detail = %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(pub, http.MethodGet, "/api/registry/missing", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing package = %d", rec.Code)
	}

	consumer, eng, root := registryProject(t, registryDir, map[string]string{
		"pipelines/other.yaml": "name: other\nentry: a\nagents:\n  - id: a\n    runtime: mock\n",
	})
	if rec := call(consumer, http.MethodPost, "/api/registry/install", `{"ref":"notes","digest":"sha256:wrong"}`); rec.Code != http.StatusConflict {
		t.Fatalf("pinned install = %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(consumer, http.MethodPost, "/api/registry/install", `{"ref":"notes@2.0.0","digest":"`+detail.Manifest.Digest+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("install = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "pipelines", "notes.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Start("notes", "go"); err != nil {
		t.Fatalf("installed pipeline not loaded: %v", err)
	}
	var installed struct {
		Installed map[string]struct {
			Version string `json:"version"`
		} `json:"installed"`
	}
	rec = call(consumer, http.MethodGet, "/api/registry", "")
	if json.Unmarshal(rec.Body.Bytes(), &installed) != nil || installed.Installed["notes"].Version != "2.0.0" {
		t.Fatalf("installed = %s", rec.Body.String())
	}
}
