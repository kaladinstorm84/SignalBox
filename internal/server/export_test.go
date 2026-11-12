package server

import (
	"bytes"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/exporter"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
)

func TestDownloadPythonExportMatchesCLIAndRejectsUnsupportedPipelines(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, config.FileName)
	if err := os.WriteFile(configPath, []byte("version: 1\nruntimes:\n  agent:\n    type: codex\n    timeout_seconds: 1000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "pipelines")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "article.yaml")
	yaml := "name: article\nagents:\n  - id: writer\n    runtime: agent\n    prompt: Write an article\n"
	if err := os.WriteFile(file, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	load := func() (map[string]*pipeline.Pipeline, error) {
		ps, errs := pipeline.LoadDir(dir)
		if len(errs) > 0 {
			return nil, errs[0]
		}
		return ps, nil
	}
	handler := New(nil, "test", load, nil, nil, root).Handler()
	get := func(url string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, url, nil))
		return response
	}
	response := get("/api/pipelines/article/export?format=python")
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/x-python; charset=utf-8" ||
		response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download: %d %v\n%s", response.Code, response.Header(), response.Body.String())
	}
	disposition, parameters, err := mime.ParseMediaType(response.Header().Get("Content-Disposition"))
	if err != nil || disposition != "attachment" || parameters["filename"] != "article.py" {
		t.Fatalf("download filename: %q %v", response.Header().Get("Content-Disposition"), err)
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	ps, err := load()
	if err != nil {
		t.Fatal(err)
	}
	want, err := exporter.Python(ps["article"], cfg)
	if err != nil || !bytes.Equal(response.Body.Bytes(), want) {
		t.Fatalf("UI and CLI export differ: %v", err)
	}
	changed := strings.Replace(yaml, "Write an article", "Write the updated saved article", 1)
	if err := os.WriteFile(file, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	updated := get("/api/pipelines/article/export")
	if updated.Code != http.StatusOK || bytes.Equal(updated.Body.Bytes(), response.Body.Bytes()) {
		t.Fatal("export did not reload saved YAML")
	}
	if response := get("/api/pipelines/missing/export"); response.Code != http.StatusNotFound {
		t.Fatalf("missing pipeline: %d", response.Code)
	}
	if response := get("/api/pipelines/article/export?format=javascript"); response.Code != http.StatusBadRequest {
		t.Fatalf("unsupported format: %d", response.Code)
	}
	parallel := "name: article\nagents:\n  - id: writer\n    runtime: agent\n    handoff: [a, b]\n  - id: a\n    runtime: agent\n  - id: b\n    runtime: agent\n"
	if err := os.WriteFile(file, []byte(parallel), 0o600); err != nil {
		t.Fatal(err)
	}
	if response := get("/api/pipelines/article/export"); response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "parallel") || response.Header().Get("Content-Disposition") != "" {
		t.Fatalf("unsupported pipeline downloaded: %d %s", response.Code, response.Body.String())
	}
}
