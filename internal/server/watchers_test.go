package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/config"
)

func TestWatchersConfigurationAPI(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, config.FileName)
	original := "# project comment\r\nversion: 1\r\nproject: demo\r\nruntimes:\r\n  mock:\r\n    type: mock\r\n\r\n# keep before watchers\r\nwatchers:\r\n  - name: first # keep name comment\r\n    command: echo\r\n    args: [hello]\r\n    interval: 5m\r\n    pipeline: demo\r\n\r\n# keep next section\r\nmax_concurrent_calls: 2\r\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "pipelines"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pipelines", "demo.yaml"), []byte("name: demo\nagents:\n  - id: worker\n    runtime: mock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := New(nil, "demo", nil, nil, nil, root).Handler()
	get := func() watcherDocument {
		t.Helper()
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/watchers", nil))
		if res.Code != http.StatusOK {
			t.Fatalf("GET: %d %s", res.Code, res.Body)
		}
		var doc watcherDocument
		if err := json.Unmarshal(res.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	put := func(doc watcherDocument) *httptest.ResponseRecorder {
		t.Helper()
		data, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(http.MethodPut, "/api/watchers", bytes.NewReader(data)))
		return res
	}
	doc := get()
	if len(doc.Watchers) != 1 || doc.Watchers[0].Name != "first" {
		t.Fatalf("unexpected watchers: %+v", doc.Watchers)
	}
	doc.Watchers[0].Name = "renamed"
	doc.Watchers[0].Args = []string{"a b", "--flag"}
	doc.Watchers = append(doc.Watchers, watcherEntry{Name: "second", Command: "echo", Interval: "1m", Pipeline: "demo"})
	if res := put(doc); res.Code != http.StatusOK {
		t.Fatalf("save: %d %s", res.Code, res.Body)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"# project comment\r\nversion: 1", "# keep before watchers\r\nwatchers:", "# keep name comment", "# keep next section\r\nmax_concurrent_calls: 2"} {
		if !strings.Contains(string(saved), text) {
			t.Fatalf("missing %q in %s", text, saved)
		}
		if strings.Count(string(saved), "# keep next section") != 1 {
			t.Fatalf("duplicated comment: %s", saved)
		}
	}
	if strings.Contains(strings.ReplaceAll(string(saved), "\r\n", ""), "\n") {
		t.Fatalf("changed line endings: %q", saved)
	}
	if res := put(doc); res.Code != http.StatusConflict {
		t.Fatalf("stale save: %d %s", res.Code, res.Body)
	}
	doc = get()
	if len(doc.Watchers) != 2 || doc.Watchers[0].Name != "renamed" || doc.Watchers[0].Args[0] != "a b" {
		t.Fatalf("saved watchers: %+v", doc.Watchers)
	}
	invalid := doc
	invalid.Watchers = append([]watcherEntry(nil), doc.Watchers...)
	invalid.Watchers[0].Pipeline = "missing"
	if res := put(invalid); res.Code != http.StatusBadRequest {
		t.Fatalf("invalid target: %d %s", res.Code, res.Body)
	}
	doc.Watchers = doc.Watchers[1:]
	if res := put(doc); res.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", res.Code, res.Body)
	}
	doc = get()
	doc.Watchers = []watcherEntry{}
	if res := put(doc); res.Code != http.StatusOK {
		t.Fatalf("delete all: %d %s", res.Code, res.Body)
	}
	if got := get().Watchers; len(got) != 0 {
		t.Fatalf("watchers after deletion: %+v", got)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/watchers", strings.NewReader("{}"))
	req.Header.Set("Origin", "https://unrelated.example")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("cross-origin save: %d", res.Code)
	}
}

func TestAddWatchersPreservesDocumentEnd(t *testing.T) {
	input := []byte("---\nversion: 1\nproject: demo\nruntimes:\n  mock:\n    type: mock\n...\n")
	updated, err := updateWatchersYAML(input, []watcherEntry{{Name: "watch", Command: "echo", Interval: "1m", Pipeline: "demo"}})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasSuffix(string(updated), "...\n") || !strings.Contains(string(updated), "watchers:") {
		t.Fatalf("unexpected YAML: %s", updated)
	}
	if _, err := config.Parse(updated, t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestWatcherYAMLRetainsUnchangedArgumentComments(t *testing.T) {
	input := []byte("version: 1\nwatchers:\n  - name: first\n    command: echo\n    args:\n      - hello # argument comment\n    interval: 5m\n    pipeline: demo\n\n# next section\nproject: demo\n")
	updated, err := updateWatchersYAML(input, []watcherEntry{{OriginalName: "first", Name: "first", Command: "echo", Args: []string{"hello"}, Interval: "1m", Pipeline: "demo"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(updated), "# argument comment") != 1 || strings.Count(string(updated), "# next section") != 1 {
		t.Fatalf("comments were lost or duplicated: %s", updated)
	}
}

func TestWatcherYAMLRejectsMultipleDocuments(t *testing.T) {
	input := []byte("version: 1\nwatchers: []\n---\nproject: another\n")
	if _, err := updateWatchersYAML(input, nil); err == nil {
		t.Fatal("expected multiple YAML documents to be rejected")
	}
}

func TestWatcherYAMLRetainsTrailingComments(t *testing.T) {
	input := []byte("version: 1\nwatchers:\n  - name: first\n    command: echo\n    interval: 5m\n    pipeline: demo\n\n# trailing comment\n")
	updated, err := updateWatchersYAML(input, []watcherEntry{{OriginalName: "first", Name: "first", Command: "echo", Interval: "1m", Pipeline: "demo"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(updated), "# trailing comment") != 1 {
		t.Fatalf("trailing comment was lost or duplicated: %s", updated)
	}
}
