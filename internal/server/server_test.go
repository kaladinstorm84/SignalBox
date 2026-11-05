package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/engine"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

func TestProjectSettingsReadValidateSaveAndConflict(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, config.FileName)
	const original = "# Preserve this comment\nversion: 1\nproject: demo\nruntimes:\n  mock:\n    type: mock\n"
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
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET settings = %d, headers = %v", response.Code, response.Header())
	}
	var document settingsDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Content != original {
		t.Fatalf("loaded content = %q", document.Content)
	}
	save := func(content, version string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(settingsDocument{Content: content, Version: version})
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(body)))
		return response
	}
	if response := save("runtimes: [", document.Version); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid YAML status = %d", response.Code)
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || string(unchanged) != original {
		t.Fatalf("invalid save changed the file: %q (%v)", unchanged, err)
	}
	updated := original + "max_concurrent_calls: 2\n"
	if response := save(updated, document.Version); response.Code != http.StatusOK {
		t.Fatalf("valid save = %d: %s", response.Code, response.Body.String())
	}
	saved, err := os.ReadFile(path)
	if err != nil || string(saved) != updated {
		t.Fatalf("saved file = %q (%v)", saved, err)
	}
	if response := save(original, document.Version); response.Code != http.StatusConflict {
		t.Fatalf("stale save status = %d", response.Code)
	}
	request := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader("{}"))
	request.Header.Set("Origin", "https://unrelated.example")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin save status = %d", response.Code)
	}
}

func TestPipelineFileEditor(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, config.FileName), []byte("version: 1\npipelines_dir: definitions\nruntimes:\n  mock:\n    type: mock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := New(nil, "test", nil, nil, nil, root).Handler()
	request := func(method, filename string, body any) *httptest.ResponseRecorder {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, "/api/pipeline-files"+filename, bytes.NewReader(data)))
		return response
	}
	const content = "# Keep this comment\nname: demo\nentry: start\nagents:\n  - id: start\n    runtime: mock\n"
	if response := request(http.MethodGet, "", nil); response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != "[]" {
		t.Fatalf("initial list = %d: %s", response.Code, response.Body.String())
	}
	response := request(http.MethodPut, "/demo.yaml", settingsDocument{Content: content})
	if response.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", response.Code, response.Body.String())
	}
	var saved settingsDocument
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "definitions", "demo.yaml")
	data, err := os.ReadFile(path)
	if err != nil || string(data) != content {
		t.Fatalf("created content = %q (%v)", data, err)
	}
	if response := request(http.MethodPut, "/demo.yaml", settingsDocument{Content: content}); response.Code != http.StatusConflict {
		t.Fatalf("overwrite without version = %d", response.Code)
	}
	if response := request(http.MethodPut, "/other.yml", settingsDocument{Content: content}); response.Code != http.StatusConflict {
		t.Fatalf("duplicate name = %d", response.Code)
	}
	if response := request(http.MethodPut, "/demo.yaml", settingsDocument{Content: "name: [", Version: saved.Version}); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid YAML = %d", response.Code)
	}
	unknownRuntime := strings.Replace(content, "runtime: mock", "runtime: missing", 1)
	if response := request(http.MethodPut, "/demo.yaml", settingsDocument{Content: unknownRuntime, Version: saved.Version}); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown runtime = %d", response.Code)
	}
	updated := content + "description: Updated from the editor\n"
	if response := request(http.MethodPut, "/demo.yaml", settingsDocument{Content: updated, Version: saved.Version}); response.Code != http.StatusOK {
		t.Fatalf("edit = %d: %s", response.Code, response.Body.String())
	}
	if response := request(http.MethodPut, "/demo.yaml", settingsDocument{Content: content, Version: saved.Version}); response.Code != http.StatusConflict {
		t.Fatalf("stale edit = %d", response.Code)
	}
	if response := request(http.MethodGet, "/demo.yaml", nil); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Updated from the editor") {
		t.Fatalf("load = %d: %s", response.Code, response.Body.String())
	}
	if err := os.WriteFile(path, []byte("name: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if response := request(http.MethodGet, "/demo.yaml", nil); response.Code != http.StatusOK {
		t.Fatalf("load invalid file for repair = %d", response.Code)
	}
	if response := request(http.MethodGet, "", nil); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "demo.yaml") {
		t.Fatalf("list invalid file = %d: %s", response.Code, response.Body.String())
	}
	for _, filename := range []string{"../outside.yaml", `..\outside.yaml`, "C:outside.yaml", "demo.txt", ".yaml"} {
		if validPipelineFilename(filename) {
			t.Fatalf("unsafe filename accepted: %q", filename)
		}
	}
	if response := request(http.MethodPut, "/..%5Coutside.yaml", settingsDocument{Content: content}); response.Code != http.StatusBadRequest {
		t.Fatalf("path escape = %d", response.Code)
	}
	foreign := httptest.NewRequest(http.MethodPut, "/api/pipeline-files/demo.yaml", strings.NewReader("{}"))
	foreign.Header.Set("Origin", "https://unrelated.example")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, foreign)
	if response.Code != http.StatusForbidden {
		t.Fatalf("foreign origin = %d", response.Code)
	}
}

func TestListPipelinesReloadsFromDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.yaml")
	writePipeline := func(path, name, entry string) {
		t.Helper()
		data := fmt.Sprintf("name: %s\nentry: %s\nagents:\n  - id: %s\n    runtime: mock\n", name, entry, entry)
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	load := func() (map[string]*pipeline.Pipeline, error) {
		ps, errs := pipeline.LoadDir(dir)
		if len(errs) != 0 {
			return nil, errs[0]
		}
		return ps, nil
	}
	writePipeline(path, "demo", "original")
	initial, err := load()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{
		"mock": {Type: "mock"},
	})
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(initial, registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(eng, "test", load, nil, nil).Handler()
	list := func(wantStatus int) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/pipelines", nil))
		if response.Code != wantStatus {
			t.Fatalf("status = %d, want %d: %s", response.Code, wantStatus, response.Body.String())
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control = %q, want no-store", got)
		}
		return response
	}
	assertListed := func(want map[string]string) {
		t.Helper()
		response := list(http.StatusOK)
		var listed []pipelineView
		if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
			t.Fatal(err)
		}
		if len(listed) != len(want) {
			t.Fatalf("listed %d pipelines, want %d", len(listed), len(want))
		}
		for _, p := range listed {
			if p.Entry != want[p.Name] {
				t.Fatalf("pipeline %q entry = %q, want %q", p.Name, p.Entry, want[p.Name])
			}
		}
	}

	assertListed(map[string]string{"demo": "original"})
	writePipeline(path, "demo", "updated")
	qaPath := filepath.Join(dir, "QA.yaml")
	writePipeline(qaPath, "QA", "requirements")
	assertListed(map[string]string{"demo": "updated", "QA": "requirements"})
	if eng.Pipelines()["demo"].Entry != "updated" || eng.Pipelines()["QA"] == nil {
		t.Fatal("engine did not receive reloaded definitions")
	}

	if err := os.WriteFile(path, []byte("name: [invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	response := list(http.StatusInternalServerError)
	var failure map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(failure["error"], "reloading pipelines:") {
		t.Fatalf("unexpected error: %v", failure)
	}
	if eng.Pipelines()["demo"].Entry != "updated" {
		t.Fatal("failed reload replaced the last valid definitions")
	}

	writePipeline(path, "demo", "fixed")
	if err := os.Remove(qaPath); err != nil {
		t.Fatal(err)
	}
	assertListed(map[string]string{"demo": "fixed"})
	if eng.Pipelines()["QA"] != nil {
		t.Fatal("removed pipeline still exists in the engine")
	}
}

func TestRetryEndpointResumesFailedRun(t *testing.T) {
	p, err := pipeline.Parse([]byte(`
name: demo
entry: a
agents:
  - id: a
    runtime: flaky
`))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	registry.Register(&runtime.Mock{RuntimeName: "flaky", Handler: func(_ context.Context, _ runtime.Request) (string, error) {
		calls++
		if calls == 1 {
			return "", fmt.Errorf("down")
		}
		return "ok", nil
	}})
	eng, err := engine.New(map[string]*pipeline.Pipeline{"demo": p}, registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(eng, "test", func() (map[string]*pipeline.Pipeline, error) {
		return map[string]*pipeline.Pipeline{"demo": p}, nil
	}, nil, nil).Handler()
	id, err := eng.Start("demo", "x")
	if err != nil {
		t.Fatal(err)
	}
	var failed []*engine.NodeRun
	for deadline := time.Now().Add(5 * time.Second); len(failed) == 0 && time.Now().Before(deadline); {
		failed = eng.WaitingRetries(id)
		time.Sleep(10 * time.Millisecond)
	}
	if len(failed) != 1 {
		t.Fatal("run did not await retry")
	}
	post := func(body string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/executions/"+id+"/retry", strings.NewReader(body)))
		return response
	}
	if response := post(`{}`); response.Code != http.StatusBadRequest {
		t.Fatalf("missing run_id status = %d", response.Code)
	}
	if response := post(`{"run_id":"nope"}`); response.Code != http.StatusConflict {
		t.Fatalf("unknown run status = %d", response.Code)
	}
	if response := post(`{"run_id":"` + failed[0].ID + `"}`); response.Code != http.StatusOK {
		t.Fatalf("retry status = %d: %s", response.Code, response.Body.String())
	}
	done, _ := eng.Wait(id)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("execution did not finish after retry")
	}
	exec, _ := eng.Get(id)
	if exec.Status != engine.StatusCompleted || exec.FinalOutputs[0].Output != "ok" {
		t.Fatalf("exec = %s %+v", exec.Status, exec.FinalOutputs)
	}
}

func TestManualEditEndpointResumesNode(t *testing.T) {
	p, err := pipeline.Parse([]byte(`
name: demo
entry: edit
agents:
  - id: edit
    type: manual_edit
    handoff: [next]
  - id: next
    runtime: mock
`))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	registry.Register(&runtime.Mock{RuntimeName: "mock", Handler: func(_ context.Context, req runtime.Request) (string, error) {
		return req.Input, nil
	}})
	eng, err := engine.New(map[string]*pipeline.Pipeline{"demo": p}, registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(eng, "test", func() (map[string]*pipeline.Pipeline, error) {
		return map[string]*pipeline.Pipeline{"demo": p}, nil
	}, nil, nil).Handler()
	id, err := eng.Start("demo", "original")
	if err != nil {
		t.Fatal(err)
	}
	var waiting []*engine.NodeRun
	for deadline := time.Now().Add(5 * time.Second); len(waiting) == 0 && time.Now().Before(deadline); {
		waiting = eng.WaitingManualEdits(id)
		time.Sleep(10 * time.Millisecond)
	}
	if len(waiting) != 1 {
		t.Fatal("run did not await manual edit")
	}
	post := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/executions/"+id+"/manual-edit", strings.NewReader(body)))
		return response
	}
	if response := post(`{}`); response.Code != http.StatusBadRequest {
		t.Fatalf("missing run_id status = %d", response.Code)
	}
	if response := post(`{"run_id":"nope","output":"revised"}`); response.Code != http.StatusConflict {
		t.Fatalf("unknown run status = %d", response.Code)
	}
	if response := post(`{"run_id":"` + waiting[0].ID + `","output":"revised","edited_by":"tester"}`); response.Code != http.StatusOK {
		t.Fatalf("manual edit status = %d: %s", response.Code, response.Body.String())
	}
	done, err := eng.Wait(id)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("execution did not finish after manual edit")
	}
	exec, err := eng.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if exec.Status != engine.StatusCompleted || len(exec.FinalOutputs) != 1 || exec.FinalOutputs[0].Output != "revised" {
		t.Fatalf("execution = %+v", exec)
	}
}

func TestStartExecutionReloadsPipelines(t *testing.T) {
	oldPipeline, err := pipeline.Parse([]byte(`
name: demo
entry: old
agents:
  - id: old
    runtime: mock
`))
	if err != nil {
		t.Fatal(err)
	}
	newPipeline, err := pipeline.Parse([]byte(`
name: demo
entry: updated
agents:
  - id: updated
    runtime: mock
`))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{
		"mock": {Type: "mock"},
	})
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(map[string]*pipeline.Pipeline{"demo": oldPipeline}, registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(eng, "test", func() (map[string]*pipeline.Pipeline, error) {
		return map[string]*pipeline.Pipeline{"demo": newPipeline}, nil
	}, nil, nil)

	listResponse := httptest.NewRecorder()
	srv.Handler().ServeHTTP(listResponse, httptest.NewRequest("GET", "/api/pipelines", nil))
	if listResponse.Code != 200 {
		t.Fatalf("GET /api/pipelines status = %d: %s", listResponse.Code, listResponse.Body.String())
	}
	var listed []pipelineView
	if err := json.Unmarshal(listResponse.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Entry != "updated" {
		t.Fatalf("listed pipelines = %+v, want entry updated", listed)
	}

	startResponse := httptest.NewRecorder()
	srv.Handler().ServeHTTP(startResponse, httptest.NewRequest("POST", "/api/executions", strings.NewReader(`{"pipeline":"demo","input":"hello"}`)))
	if startResponse.Code != 201 {
		t.Fatalf("POST /api/executions status = %d: %s", startResponse.Code, startResponse.Body.String())
	}
	var started struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(startResponse.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	done, err := eng.Wait(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("execution did not finish")
	}
	exec, err := eng.Get(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(exec.Runs) != 1 || exec.Runs[0].NodeID != "updated" {
		t.Fatalf("execution runs = %+v, want updated entry node", exec.Runs)
	}
}

func TestFormattedNodeOutputIsRenderedSafely(t *testing.T) {
	p, err := pipeline.Parse([]byte(`
name: review
entry: judge
agents:
  - id: judge
    runtime: mock
    format: true
`))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	const raw = "# Review\n\n**Approved**\n\n<script>alert(1)</script>"
	registry.Register(&runtime.Mock{RuntimeName: "mock", Handler: func(_ context.Context, _ runtime.Request) (string, error) {
		return raw, nil
	}})
	eng, err := engine.New(map[string]*pipeline.Pipeline{"review": p}, registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(eng, "test", func() (map[string]*pipeline.Pipeline, error) {
		return map[string]*pipeline.Pipeline{"review": p}, nil
	}, nil, nil).Handler()
	id, err := eng.Start("review", "review this")
	if err != nil {
		t.Fatal(err)
	}
	done, _ := eng.Wait(id)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("execution did not finish")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/executions/"+id, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET execution status = %d: %s", response.Code, response.Body.String())
	}
	var exec engine.Execution
	if err := json.Unmarshal(response.Body.Bytes(), &exec); err != nil {
		t.Fatal(err)
	}
	if len(exec.Runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(exec.Runs))
	}
	run := exec.Runs[0]
	if !run.Format || run.Output != raw {
		t.Fatalf("format/raw output = %v / %q", run.Format, run.Output)
	}
	if !strings.Contains(run.OutputHTML, "<h1>Review</h1>") || !strings.Contains(run.OutputHTML, "<strong>Approved</strong>") {
		t.Fatalf("Markdown was not rendered: %s", run.OutputHTML)
	}
	if strings.Contains(run.OutputHTML, "<script") {
		t.Fatalf("unsafe raw HTML was rendered: %s", run.OutputHTML)
	}
}

func TestRestartInterruptedRunEndpoint(t *testing.T) {
	store, err := engine.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := pipeline.Parse([]byte(`
name: demo
entry: worker
agents:
  - id: worker
    runtime: mock
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&engine.Execution{
		ID:        "20261105-000001-bbbb",
		Pipeline:  "demo",
		Input:     "original task",
		Status:    engine.StatusRunning,
		CreatedAt: time.Now().UTC(),
		Runs: []*engine.NodeRun{
			{ID: "worker-run", NodeID: "worker", Type: pipeline.TypeStandard, Status: engine.StatusRunning, Input: "saved call input"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	registry.Register(&runtime.Mock{RuntimeName: "mock", Handler: func(_ context.Context, req runtime.Request) (string, error) {
		if req.Input != "saved call input" {
			t.Errorf("retry input = %q", req.Input)
		}
		return "recovered", nil
	}})
	eng, err := engine.New(map[string]*pipeline.Pipeline{"demo": p}, registry, store)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(eng, "test", func() (map[string]*pipeline.Pipeline, error) {
		return map[string]*pipeline.Pipeline{"demo": p}, nil
	}, nil, nil).Handler()
	post := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/executions/20261105-000001-bbbb/restart", strings.NewReader(body)))
		return response
	}
	if response := post(`{}`); response.Code != http.StatusBadRequest {
		t.Fatalf("missing run_id status = %d", response.Code)
	}
	response := post(`{"run_id":"worker-run"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("restart status = %d: %s", response.Code, response.Body.String())
	}
	var started struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	done, err := eng.Wait(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("restarted execution did not finish")
	}
	exec, err := eng.Get(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if exec.Status != engine.StatusCompleted || exec.Runs[0].Input != "saved call input" {
		t.Fatalf("restarted execution = %+v", exec)
	}
}

func TestRerunAndDeleteExecution(t *testing.T) {
	p, err := pipeline.Parse([]byte(`
name: demo
entry: a
agents:
  - id: a
    runtime: gate
`))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	registry.Register(&runtime.Mock{RuntimeName: "gate", Handler: func(ctx context.Context, req runtime.Request) (string, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return "out:" + req.Input, nil
	}})
	stateDir := t.TempDir()
	store, err := engine.NewStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(map[string]*pipeline.Pipeline{"demo": p}, registry, store)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(eng, "test", func() (map[string]*pipeline.Pipeline, error) {
		return map[string]*pipeline.Pipeline{"demo": p}, nil
	}, nil, nil).Handler()
	do := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
		return response
	}
	wait := func(id string) {
		t.Helper()
		done, err := eng.Wait(id)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("execution %s did not finish", id)
		}
	}

	id, err := eng.Start("demo", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if response := do(http.MethodDelete, "/api/executions/"+id); response.Code != http.StatusConflict {
		t.Fatalf("delete running status = %d: %s", response.Code, response.Body.String())
	}

	response := do(http.MethodPost, "/api/executions/"+id+"/rerun")
	if response.Code != http.StatusCreated {
		t.Fatalf("rerun status = %d: %s", response.Code, response.Body.String())
	}
	var rerun struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &rerun); err != nil {
		t.Fatal(err)
	}
	if rerun.ID == "" || rerun.ID == id {
		t.Fatalf("rerun id = %q", rerun.ID)
	}
	close(release)
	wait(id)
	wait(rerun.ID)
	again, err := eng.Get(rerun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Pipeline != "demo" || again.Input != "hello" || again.FinalOutputs[0].Output != "out:hello" {
		t.Fatalf("rerun = %+v", again)
	}

	if response := do(http.MethodDelete, "/api/executions/"+id); response.Code != http.StatusOK {
		t.Fatalf("delete status = %d: %s", response.Code, response.Body.String())
	}
	if response := do(http.MethodGet, "/api/executions/"+id); response.Code != http.StatusNotFound {
		t.Fatalf("get deleted status = %d", response.Code)
	}
	if response := do(http.MethodDelete, "/api/executions/"+id); response.Code != http.StatusNotFound {
		t.Fatalf("delete twice status = %d", response.Code)
	}
	if response := do(http.MethodPost, "/api/executions/"+id+"/rerun"); response.Code != http.StatusNotFound {
		t.Fatalf("rerun deleted status = %d", response.Code)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "executions", id+".json")); !os.IsNotExist(err) {
		t.Fatalf("execution file still exists: %v", err)
	}
	for _, s := range eng.List() {
		if s.ID == id {
			t.Fatal("deleted execution still listed")
		}
	}

	reloaded, err := engine.New(map[string]*pipeline.Pipeline{"demo": p}, registry, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.Get(id); err == nil {
		t.Fatal("deleted execution came back after restart")
	}
	if _, err := reloaded.Get(rerun.ID); err != nil {
		t.Fatalf("rerun execution missing after restart: %v", err)
	}
}
