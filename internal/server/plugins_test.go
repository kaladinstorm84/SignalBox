package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegistryPluginEndpoints(t *testing.T) {
	dir := t.TempDir()
	pub, _, _ := registryProject(t, dir, map[string]string{
		"pipelines/demo.yaml":         "name: demo\nagents:\n  - id: a\n    type: plugin:example\n",
		"plugins/example/plugin.yaml": "language: python\nentry: main.py\n",
		"plugins/example/main.py":     "print('ok')\n",
		"plugins/example/asset.bin":   "\x00\xff",
	})
	call := func(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	rec := call(pub, http.MethodGet, "/api/registry", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"publishable_plugins":["example"]`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(pub, http.MethodPost, "/api/registry/publish", `{"plugin":"example","version":"1.0.0"}`)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"kind":"plugin"`) {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(pub, http.MethodGet, "/api/registry/example/1.0.0", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Binary file: 2 bytes") {
		t.Fatalf("inspect: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(pub, http.MethodGet, "/api/pipelines", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"type":"plugin:example"`) || !strings.Contains(rec.Body.String(), `"plugin":`) {
		t.Fatalf("node metadata: %d %s", rec.Code, rec.Body.String())
	}
	consumer, _, _ := registryProject(t, dir, map[string]string{
		"pipelines/other.yaml": "name: other\nagents:\n  - id: a\n    runtime: mock\n",
	})
	rec = call(consumer, http.MethodPost, "/api/registry/install", `{"ref":"example@1.0.0"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("install: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(pub, http.MethodPost, "/api/registry/publish", `{"pipeline":"demo","plugin":"example"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ambiguous request: %d %s", rec.Code, rec.Body.String())
	}
}
