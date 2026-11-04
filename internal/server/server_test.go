package server

import (
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

	"github.com/gitea/signalbox/internal/config"
	"github.com/gitea/signalbox/internal/engine"
	"github.com/gitea/signalbox/internal/pipeline"
	"github.com/gitea/signalbox/internal/runtime"
)

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
