package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/engine"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

func newUploadServer(t *testing.T) (http.Handler, *engine.Engine) {
	t.Helper()
	p, err := pipeline.Parse([]byte("name: up\nfiles: {extensions: [.txt, .html]}\nagents:\n  - id: a\n    runtime: mock\n    files: inline\n"))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	store, err := engine.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	pipelines := map[string]*pipeline.Pipeline{"up": p}
	eng, err := engine.New(pipelines, registry, store)
	if err != nil {
		t.Fatal(err)
	}
	eng.SetUploadLimits(engine.UploadLimits{MaxBytes: 64, MaxFiles: 2, AllowedExtensions: []string{".txt", ".html"}})
	return New(eng, "test", func() (map[string]*pipeline.Pipeline, error) { return pipelines, nil }, nil, nil, t.TempDir()).Handler(), eng
}

func multipartStart(t *testing.T, fields map[string]string, files map[string]string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range files {
		part, err := mw.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		part.Write([]byte(content))
	}
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/executions", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestMultipartStartAndDownload(t *testing.T) {
	handler, eng := newUploadServer(t)

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, multipartStart(t, map[string]string{"pipeline": "up", "input": "go", "params": "{}"},
		map[string]string{"notes.txt": "hello", "page.html": "<script>alert(1)</script>"}))
	if resp.Code != http.StatusCreated && resp.Code != http.StatusOK {
		t.Fatalf("start = %d: %s", resp.Code, resp.Body.String())
	}
	var started struct {
		ID string `json:"id"`
	}
	json.Unmarshal(resp.Body.Bytes(), &started)
	done, err := eng.Wait(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("execution did not finish")
	}
	x, err := eng.Get(started.ID)
	if err != nil || len(x.Files) != 2 {
		t.Fatalf("files = %+v %v", x, err)
	}

	get := func(name string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/executions/"+started.ID+"/files/"+name, nil))
		return r
	}
	r := get("notes.txt")
	if r.Code != http.StatusOK || r.Body.String() != "hello" || !strings.HasPrefix(r.Header().Get("Content-Disposition"), "attachment") ||
		r.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(r.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("download = %d %v %q", r.Code, r.Header(), r.Body.String())
	}
	// HTML is never served as HTML.
	r = get("page.html")
	if ct := r.Header().Get("Content-Type"); r.Code != http.StatusOK || strings.Contains(ct, "html") || !strings.Contains(r.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("html download headers = %v", r.Header())
	}
	for _, name := range []string{"missing.txt", "..%2Fsignalbox.yaml", "%2E%2E"} {
		if r := get(name); r.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d", name, r.Code)
		}
	}

	meta := httptest.NewRecorder()
	handler.ServeHTTP(meta, httptest.NewRequest(http.MethodGet, "/api/meta", nil))
	if !strings.Contains(meta.Body.String(), `"max_files":2`) || !strings.Contains(meta.Body.String(), `"max_bytes":64`) {
		t.Fatalf("meta = %s", meta.Body.String())
	}
}

func TestMultipartStartRejectsBadUploads(t *testing.T) {
	handler, eng := newUploadServer(t)
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"too large", map[string]string{"big.txt": strings.Repeat("x", 65)}, "max_bytes"},
		{"too many", map[string]string{"a.txt": "a", "b.txt": "b", "c.txt": "c"}, "max_files"},
		{"extension", map[string]string{"a.exe": "a"}, "not allowed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, multipartStart(t, map[string]string{"pipeline": "up"}, tc.files))
			if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), tc.want) {
				t.Fatalf("status %d: %s", resp.Code, resp.Body.String())
			}
		})
	}
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, multipartStart(t, map[string]string{"pipeline": "up", "params": "{not json"}, map[string]string{"a.txt": "a"}))
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("bad params status %d", resp.Code)
	}
	if n := len(eng.List()); n != 0 {
		t.Fatalf("%d executions created by rejected uploads", n)
	}
}
