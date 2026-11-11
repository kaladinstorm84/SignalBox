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
	stateDir := t.TempDir()
	store, err := engine.NewStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
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
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = engine.NewStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
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
	if response := post(`{"run_id":"missing"}`); response.Code != http.StatusConflict {
		t.Fatalf("unknown run status = %d", response.Code)
	}
	// With no run_id the interrupted run is detected automatically.
	response := post(`{}`)
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
	if exec.Recovery == nil || exec.Recovery.SourceExecution != "20261105-000001-bbbb" || exec.Recovery.SourceRun != "worker-run" {
		t.Fatalf("recovery = %+v", exec.Recovery)
	}

	// Restarting the restarted execution after an edit needs force.
	changed, err := pipeline.Parse([]byte("name: demo\nentry: worker\nagents:\n  - id: worker\n    runtime: mock\n    prompt: new prompt\n"))
	if err != nil {
		t.Fatal(err)
	}
	p = changed
	conflict := httptest.NewRecorder()
	handler.ServeHTTP(conflict, httptest.NewRequest(http.MethodPost, "/api/executions/"+started.ID+"/restart", strings.NewReader(`{"node_id":"worker"}`)))
	var body struct {
		Changed bool     `json:"definition_changed"`
		Changes []string `json:"changes"`
	}
	if conflict.Code != http.StatusConflict || json.Unmarshal(conflict.Body.Bytes(), &body) != nil || !body.Changed || len(body.Changes) != 1 {
		t.Fatalf("changed definition response = %d %s", conflict.Code, conflict.Body.String())
	}
	forced := httptest.NewRecorder()
	handler.ServeHTTP(forced, httptest.NewRequest(http.MethodPost, "/api/executions/"+started.ID+"/restart", strings.NewReader(`{"node_id":"worker","force":true}`)))
	if forced.Code != http.StatusCreated {
		t.Fatalf("forced restart = %d %s", forced.Code, forced.Body.String())
	}
}

func TestDashboardAPI(t *testing.T) {
	eng, err := engine.New(nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(eng, "test", nil, nil, nil).Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/dashboard", nil))
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("dashboard response = %d, headers %v", response.Code, response.Header())
	}
	var dashboard engine.Dashboard
	if err := json.Unmarshal(response.Body.Bytes(), &dashboard); err != nil {
		t.Fatal(err)
	}
	if dashboard.ActiveAgents != 0 || dashboard.InProgress != 0 || dashboard.Total != 0 ||
		dashboard.Active == nil || dashboard.Recent == nil || dashboard.Statuses == nil {
		t.Fatalf("empty dashboard = %+v", dashboard)
	}
	store, err := engine.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	eng, err = engine.New(nil, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	New(eng, "test", nil, nil, nil).Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/dashboard", nil))
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "loading dashboard") {
		t.Fatalf("failed dashboard response = %d: %s", response.Code, response.Body.String())
	}
}

func TestListExecutionsPagination(t *testing.T) {
	store, err := engine.NewStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for i := 1; i <= 25; i++ {
		if err := store.Save(&engine.Execution{
			ID:        fmt.Sprintf("run-%02d", i),
			Pipeline:  "demo",
			Input:     "task",
			Status:    engine.StatusCompleted,
			CreatedAt: time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC),
		}); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(nil, registry, store)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(eng, "test", nil, nil, nil).Handler()
	get := func(query string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/executions"+query, nil))
		return response
	}
	defaultPage := get("")
	if defaultPage.Code != http.StatusOK || defaultPage.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("default page response = %d, headers %v", defaultPage.Code, defaultPage.Header())
	}
	var defaultResult struct {
		Executions []engine.Summary `json:"executions"`
		PageSize   int              `json:"page_size"`
	}
	if err := json.Unmarshal(defaultPage.Body.Bytes(), &defaultResult); err != nil {
		t.Fatal(err)
	}
	if len(defaultResult.Executions) != 20 || defaultResult.PageSize != 20 {
		t.Fatalf("default page size = %d with %d records", defaultResult.PageSize, len(defaultResult.Executions))
	}
	response := get("?page=2&page_size=10")
	if response.Code != http.StatusOK {
		t.Fatalf("page status = %d: %s", response.Code, response.Body.String())
	}
	var result struct {
		Executions []engine.Summary `json:"executions"`
		Page       int              `json:"page"`
		PageSize   int              `json:"page_size"`
		Total      int              `json:"total"`
		TotalPages int              `json:"total_pages"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Executions) != 10 || result.Page != 2 || result.PageSize != 10 || result.Total != 25 || result.TotalPages != 3 {
		t.Fatalf("pagination result = %+v", result)
	}
	if result.Executions[0].ID != "run-15" || result.Executions[9].ID != "run-06" {
		t.Fatalf("page order = %q..%q", result.Executions[0].ID, result.Executions[9].ID)
	}
	for _, query := range []string{"?page=0", "?page_size=15", "?page_size=110"} {
		if response := get(query); response.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, want bad request", query, response.Code)
		}
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
	defer store.Close()
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
	if _, err := os.Stat(filepath.Join(stateDir, "executions.db")); err != nil {
		t.Fatalf("execution database missing: %v", err)
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

func TestPipelineAPIExposesRoutingGateAndRetryConfiguration(t *testing.T) {
	p, err := pipeline.Parse([]byte(`
name: api
entry: source
agents:
  - id: source
    runtime: mock
    retry: {count: 1, backoff: 10ms}
    handoff:
      - to: quality
        condition: always
        input: "{{ output }}"
  - id: quality
    type: llm_gate
    runtime:
      provider: codex
      model: initial-model
      effort: medium
      speed: standard
    retry:
      attempts:
        - runtime: {provider: claude, model: opus, effort: high, speed: fast}
    prompt: "Check quality."
    reject-to: source
    max_reworks: 1
`))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	pipelines := map[string]*pipeline.Pipeline{p.Name: p}
	eng, err := engine.New(pipelines, registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(eng, "test", func() (map[string]*pipeline.Pipeline, error) {
		return pipelines, nil
	}, nil, nil).Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/pipelines", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET pipelines = %d: %s", response.Code, response.Body.String())
	}
	var result []struct {
		Nodes []struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Handoff []struct {
				To        string `json:"to"`
				Condition string `json:"condition"`
			} `json:"handoff"`
			Retry      *pipeline.RetryPolicy `json:"retry"`
			MaxReworks int                   `json:"max_reworks"`
			Runtime    string                `json:"runtime"`
			Model      string                `json:"model"`
			Effort     string                `json:"reasoning_effort"`
			Speed      string                `json:"speed"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || len(result[0].Nodes) != 2 {
		t.Fatalf("pipeline API response = %s", response.Body.String())
	}
	source, gate := result[0].Nodes[0], result[0].Nodes[1]
	if source.Retry == nil || source.Retry.Count != 1 || len(source.Handoff) != 1 ||
		source.Handoff[0].To != "quality" || source.Handoff[0].Condition != "always" {
		t.Fatalf("source view = %+v", source)
	}
	if gate.Type != string(pipeline.TypeLLMGate) || gate.MaxReworks != 1 {
		t.Fatalf("gate view = %+v", gate)
	}
	if gate.Runtime != "codex" || gate.Model != "initial-model" || gate.Effort != "medium" || gate.Speed != "standard" ||
		gate.Retry == nil || len(gate.Retry.Attempts) != 1 {
		t.Fatalf("grouped runtime view = %+v", gate)
	}
	choice := gate.Retry.Attempts[0]
	if choice.Runtime != "claude" || choice.Model != "opus" || choice.ReasoningEffort != "high" || choice.Speed != "fast" {
		t.Fatalf("fallback API view = %+v", choice)
	}
}

func TestSessionPipelineAPIAndChunkEndpoints(t *testing.T) {
	p, err := pipeline.Parse([]byte(`
name: talk
entry: start
agents:
  - id: start
    runtime: mock
    handoff: [panel]
  - id: panel
    type: session
    participants: [a, b]
    max_rounds: 1
    context: {strategy: recent, max_chars: 500}
    chunks:
      enabled: true
    handoff:
      - to: after
        condition: on_no_consensus
  - id: a
    runtime: slow
  - id: b
    runtime: mock
  - id: after
    runtime: mock
`))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	registry.Register(&runtime.Mock{RuntimeName: "slow", Handler: func(ctx context.Context, _ runtime.Request) (string, error) {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return "thinking", nil
	}})
	pipelines := map[string]*pipeline.Pipeline{p.Name: p}
	eng, err := engine.New(pipelines, registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(eng, "test", func() (map[string]*pipeline.Pipeline, error) {
		return pipelines, nil
	}, nil, nil).Handler()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, path, strings.NewReader(body)))
		return response
	}

	listed := do(http.MethodGet, "/api/pipelines", "")
	var views []struct {
		Nodes []struct {
			ID           string                  `json:"id"`
			Participants []string                `json:"participants"`
			MaxRounds    int                     `json:"max_rounds"`
			TurnOrder    string                  `json:"turn_order"`
			Consensus    *pipeline.Consensus     `json:"consensus"`
			Chunks       *pipeline.ChunkConfig   `json:"chunks"`
			Context      *pipeline.ContextConfig `json:"context"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &views); err != nil {
		t.Fatal(err)
	}
	panel := views[0].Nodes[1]
	if panel.ID != "panel" || len(panel.Participants) != 2 || panel.MaxRounds != 1 || panel.TurnOrder != "round_robin" ||
		panel.Consensus == nil || panel.Consensus.Keyword != "VOTE: AGREE" || panel.Chunks == nil || !panel.Chunks.Enabled ||
		panel.Context == nil || panel.Context.Strategy != "recent" || panel.Context.Window != 1 || panel.Context.MaxChars != 500 {
		t.Fatalf("session view = %s", listed.Body.String())
	}

	id, err := eng.Start("talk", "x")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	base := "/api/executions/" + id + "/chunks"
	if r := do(http.MethodPost, base, `{"title":""}`); r.Code != http.StatusBadRequest {
		t.Fatalf("blank title status = %d", r.Code)
	}
	if r := do(http.MethodPost, "/api/executions/missing/chunks", `{"title":"x"}`); r.Code != http.StatusNotFound {
		t.Fatalf("missing execution status = %d", r.Code)
	}
	added := do(http.MethodPost, base, `{"title":"Write tests","by":"matt"}`)
	if added.Code != http.StatusCreated || !strings.Contains(added.Body.String(), `"id":"C-001"`) {
		t.Fatalf("add = %d %s", added.Code, added.Body.String())
	}
	do(http.MethodPost, base, `{"title":"Drop me"}`)
	if r := do(http.MethodDelete, base+"/C-002", ""); r.Code != http.StatusOK {
		t.Fatalf("remove = %d %s", r.Code, r.Body.String())
	}
	if r := do(http.MethodPost, base+"/C-009/done", ""); r.Code != http.StatusNotFound {
		t.Fatalf("unknown chunk status = %d", r.Code)
	}
	if r := do(http.MethodPost, base+"/C-001/done", `{"by":"matt"}`); r.Code != http.StatusOK {
		t.Fatalf("done = %d %s", r.Code, r.Body.String())
	}
	listedChunks := do(http.MethodGet, base, "")
	var chunks []engine.Chunk
	if err := json.Unmarshal(listedChunks.Body.Bytes(), &chunks); err != nil || len(chunks) != 1 || chunks[0].Status != engine.ChunkDone || chunks[0].DoneBy != "matt" {
		t.Fatalf("chunks = %s (%v)", listedChunks.Body.String(), err)
	}

	close(release)
	done, _ := eng.Wait(id)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("execution did not finish")
	}
	if r := do(http.MethodPost, base, `{"title":"late"}`); r.Code != http.StatusConflict {
		t.Fatalf("add after finish status = %d", r.Code)
	}
	exec := do(http.MethodGet, "/api/executions/"+id, "")
	if !strings.Contains(exec.Body.String(), `"session":{`) || !strings.Contains(exec.Body.String(), `"outcome":"no_consensus"`) {
		t.Fatalf("execution JSON missing session state: %s", exec.Body.String())
	}
}

func TestParamsStartRerunAndPipelineView(t *testing.T) {
	p, err := pipeline.Parse([]byte(`
name: demo
params:
  topic: {required: true, description: What to discuss}
  depth: {type: integer, default: "2"}
entry: a
agents:
  - id: a
    runtime: mock
    model: m1
    prompt: Talk about ${{ params.topic }} at depth ${{ params.depth }}.
`))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(map[string]*pipeline.Pipeline{"demo": p}, registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(eng, "test", func() (map[string]*pipeline.Pipeline, error) {
		return map[string]*pipeline.Pipeline{"demo": p}, nil
	}, nil, nil).Handler()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, path, strings.NewReader(body)))
		return response
	}
	waitID := func(response *httptest.ResponseRecorder) *engine.Execution {
		t.Helper()
		if response.Code != http.StatusCreated {
			t.Fatalf("status = %d: %s", response.Code, response.Body.String())
		}
		var created struct{ ID string }
		if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		done, err := eng.Wait(created.ID)
		if err != nil {
			t.Fatal(err)
		}
		<-done
		exec, err := eng.Get(created.ID)
		if err != nil {
			t.Fatal(err)
		}
		return exec
	}

	if response := do(http.MethodPost, "/api/executions", `{"pipeline":"demo","input":"x"}`); response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "topic") {
		t.Fatalf("missing param = %d: %s", response.Code, response.Body.String())
	}
	if response := do(http.MethodPost, "/api/executions", `{"pipeline":"demo","params":{"topic":"t","depth":"deep"}}`); response.Code != http.StatusBadRequest {
		t.Fatalf("bad integer = %d", response.Code)
	}
	first := waitID(do(http.MethodPost, "/api/executions", `{"pipeline":"demo","input":"x","params":{"topic":"queues"}}`))
	if first.Params["topic"] != "queues" || first.Params["depth"] != "2" || first.Runs[0].Model != "m1" {
		t.Fatalf("execution = %+v / run %+v", first.Params, first.Runs[0])
	}
	rerun := waitID(do(http.MethodPost, "/api/executions/"+first.ID+"/rerun", ""))
	if rerun.Params["topic"] != "queues" {
		t.Fatalf("rerun params = %v", rerun.Params)
	}

	response := do(http.MethodGet, "/api/pipelines", "")
	var views []map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &views); err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(views[0]["params"])
	if string(params) != `[{"default":"2","name":"depth","type":"integer"},{"description":"What to discuss","name":"topic","required":true,"type":"string"}]` {
		t.Fatalf("params view = %s", params)
	}
	if node := views[0]["nodes"].([]any)[0].(map[string]any); node["model"] != "m1" {
		t.Fatalf("node view = %v", node)
	}
}

func TestSchemasAPIAndStructuredPipelineView(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		config.FileName:        "version: 1\nruntimes:\n  mock: {type: mock}\n",
		"schemas/triage.json":  `{"type":"object","required":["risk"],"properties":{"risk":{"enum":["low","high"]}}}`,
		"schemas/broken.json":  `{"type":`,
		"pipelines/typed.yaml": "name: typed\ninput: {schema: triage}\nentry: a\nagents:\n  - id: a\n    runtime: mock\n    output: {schema: triage}\n",
	}
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	pipelines, errs := pipeline.LoadDir(cfg.PipelinesPath(), pipeline.Options{Root: root, SchemasDir: cfg.SchemasPath()})
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	registry, err := runtime.NewRegistry(cfg.Runtimes)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(pipelines, registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(eng, "test", func() (map[string]*pipeline.Pipeline, error) { return pipelines, nil }, nil, nil, root).Handler()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/schemas", nil))
	var listing struct {
		Dir     string `json:"dir"`
		Schemas []struct {
			Name   string         `json:"name"`
			File   string         `json:"file"`
			Schema map[string]any `json:"schema"`
			UsedBy []string       `json:"used_by"`
		} `json:"schemas"`
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if listing.Dir != "schemas" || len(listing.Schemas) != 1 || listing.Schemas[0].File != "triage.json" ||
		listing.Schemas[0].Schema["type"] != "object" || strings.Join(listing.Schemas[0].UsedBy, ",") != "typed/input,typed/a" ||
		len(listing.Errors) != 1 || !strings.Contains(listing.Errors[0], "broken") {
		t.Fatalf("schemas = %s", response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/pipelines", nil))
	var views []struct {
		InputSchema string `json:"input_schema"`
		Nodes       []struct {
			Output *struct {
				Schema     string `json:"schema"`
				OnInvalid  string `json:"on_invalid"`
				MaxRepairs int    `json:"max_repairs"`
			} `json:"output"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &views); err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].InputSchema != "triage" || views[0].Nodes[0].Output == nil ||
		*views[0].Nodes[0].Output != (struct {
			Schema     string `json:"schema"`
			OnInvalid  string `json:"on_invalid"`
			MaxRepairs int    `json:"max_repairs"`
		}{"triage", "repair", 2}) {
		t.Fatalf("pipelines = %s", response.Body.String())
	}
}
