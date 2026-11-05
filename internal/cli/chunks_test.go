package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChunksCommandCallsServerAPI(t *testing.T) {
	type call struct{ method, path, body string }
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, call{r.Method, r.URL.Path, string(body)})
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]string{{"id": "C-001", "status": "pending", "title": "Spec", "created_by": "session:a"}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/chunks"):
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "C-002", "title": "Write the tests"})
		case strings.Contains(r.URL.Path, "C-404"):
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": `unknown chunk "C-404"`})
		default:
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		}
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	var out bytes.Buffer
	run := func(args ...string) error {
		return cmdChunks(append(args, "--addr", addr), &out)
	}
	if err := run("list", "exec1"); err != nil {
		t.Fatal(err)
	}
	if err := run("add", "exec1", "Write", "the", "tests", "--by=matt"); err != nil {
		t.Fatal(err)
	}
	if err := run("done", "exec1", "C-001"); err != nil {
		t.Fatal(err)
	}
	if err := run("remove", "exec1", "C-001"); err != nil {
		t.Fatal(err)
	}
	if err := run("done", "exec1", "C-404"); err == nil || !strings.Contains(err.Error(), "unknown chunk") {
		t.Fatalf("API error not surfaced: %v", err)
	}
	if err := run("bogus", "exec1"); err == nil {
		t.Fatal("unknown subcommand accepted")
	}

	want := []call{
		{"GET", "/api/executions/exec1/chunks", ""},
		{"POST", "/api/executions/exec1/chunks", `{"by":"matt","title":"Write the tests"}`},
		{"POST", "/api/executions/exec1/chunks/C-001/done", `{"by":"cli"}`},
		{"DELETE", "/api/executions/exec1/chunks/C-001", ""},
	}
	for i, w := range want {
		if calls[i] != w {
			t.Fatalf("call %d = %+v, want %+v", i, calls[i], w)
		}
	}
	text := out.String()
	for _, s := range []string{"C-001", "pending", "Spec", "added C-002: Write the tests", "C-001 marked done", "C-001 removed"} {
		if !strings.Contains(text, s) {
			t.Fatalf("output missing %q:\n%s", s, text)
		}
	}
}
