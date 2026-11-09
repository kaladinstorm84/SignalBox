package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/engine"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
	"github.com/kaladinstorm84/SignalBox/internal/server"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connect(t *testing.T, s *Server, opts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := s.server.Connect(ctx, serverTransport, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, opts)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		serverSession.Close()
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		session.Close()
		serverSession.Close()
		cancel()
	})
	return session
}

func call(t *testing.T, session *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func read(t *testing.T, session *mcp.ClientSession, uri string) string {
	t.Helper()
	result, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Contents) != 1 {
		t.Fatalf("resource contents = %#v", result.Contents)
	}
	return result.Contents[0].Text
}

func TestToolsResourcesAndHumanGates(t *testing.T) {
	p, err := pipeline.Parse([]byte(`
name: demo
entry: review
agents:
  - id: review
    type: approval
    handoff: [edit]
  - id: edit
    type: manual_edit
    handoff: [publish]
  - id: publish
    runtime: mock
`))
	if err != nil {
		t.Fatal(err)
	}
	pipelines := map[string]*pipeline.Pipeline{"demo": p}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	registry.Register(&runtime.Mock{RuntimeName: "mock", Handler: func(_ context.Context, req runtime.Request) (string, error) {
		return req.Input, nil
	}})
	eng, err := engine.New(pipelines, registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(server.New(eng, "test", func() (map[string]*pipeline.Pipeline, error) {
		return pipelines, nil
	}, nil, nil).Handler())
	defer api.Close()
	s, err := New(context.Background(), Options{URL: api.URL, Version: "test", AllowReview: true})
	if err != nil {
		t.Fatal(err)
	}
	session := connect(t, s, nil)
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 10 {
		t.Fatalf("tools = %d, want 10", len(tools.Tools))
	}
	for _, tool := range tools.Tools {
		if tool.InputSchema == nil || tool.OutputSchema == nil || tool.Annotations == nil ||
			tool.Annotations.ReadOnlyHint || tool.Annotations.IdempotentHint {
			t.Fatalf("missing schema or unsafe annotations: %#v", tool)
		}
	}
	if got := read(t, session, "signalbox://pipelines/demo"); !strings.Contains(got, `"name":"demo"`) {
		t.Fatalf("pipeline = %s", got)
	}
	result := call(t, session, "start_execution", map[string]any{"pipeline": "demo", "input": "proposal"})
	if result.IsError || result.StructuredContent == nil {
		t.Fatalf("start = %#v", result)
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var started commandOutput
	if err := json.Unmarshal(raw, &started); err != nil {
		t.Fatal(err)
	}
	if started.ExecutionID == "" || started.ResourceURI != executionURI(started.ExecutionID) ||
		started.Dashboard != api.URL+"/#/exec/"+started.ExecutionID {
		t.Fatalf("start output = %#v", started)
	}
	foundLink := false
	for _, content := range result.Content {
		if link, ok := content.(*mcp.ResourceLink); ok && link.URI == started.ResourceURI {
			foundLink = true
		}
	}
	if !foundLink {
		t.Fatal("start result has no execution resource link")
	}
	wait := func(status engine.Status) *engine.Execution {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			execution, err := eng.Get(started.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			if execution.Status == status {
				return execution
			}
			for _, run := range execution.Runs {
				if run.Status == status && !status.Terminal() {
					return execution
				}
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("execution did not reach %s", status)
		return nil
	}
	execution := wait(engine.StatusWaitingApproval)
	runID := execution.Runs[0].ID
	if got := read(t, session, started.ResourceURI+"/approvals"); !strings.Contains(got, `"run_id":"`+runID+`"`) ||
		!strings.Contains(got, `"input":"proposal"`) || !strings.Contains(got, `"total":1`) {
		t.Fatalf("pending approvals = %s", got)
	}
	result = call(t, session, "resolve_approval", map[string]any{
		"execution_id": execution.ID, "run_id": runID, "approved": true,
	})
	if result.IsError {
		t.Fatalf("approval failed: %#v", result)
	}
	execution = wait(engine.StatusWaitingManualEdit)
	if got := read(t, session, started.ResourceURI+"/approvals"); !strings.Contains(got, `"approvals":[]`) ||
		!strings.Contains(got, `"total":0`) {
		t.Fatalf("resolved approval or manual edit listed as pending approval: %s", got)
	}
	editID := execution.Runs[len(execution.Runs)-1].ID
	result = call(t, session, "submit_manual_edit", map[string]any{
		"execution_id": execution.ID, "run_id": editID, "output": "revised",
	})
	if result.IsError {
		t.Fatalf("edit failed: %#v", result)
	}
	execution = wait(engine.StatusCompleted)
	if execution.Runs[0].ApprovedBy != "mcp-client" {
		t.Fatalf("approval identity = %q", execution.Runs[0].ApprovedBy)
	}
	if got := read(t, session, started.ResourceURI+"/results"); !strings.Contains(got, `"output":"revised"`) {
		t.Fatalf("results = %s", got)
	}
	lastRun := execution.Runs[len(execution.Runs)-1].ID
	outputURI := started.ResourceURI + "/runs/" + lastRun + "/output?offset=1&limit=3"
	if got := read(t, session, outputURI); !strings.Contains(got, `"output":"evi"`) {
		t.Fatalf("output page = %s", got)
	}
	if got := read(t, session, "signalbox://executions?page=1&page_size=10"); !strings.Contains(got, `"page_size":10`) {
		t.Fatalf("execution list = %s", got)
	}
	result = call(t, session, "rerun_execution", map[string]any{"execution_id": execution.ID})
	if result.IsError {
		t.Fatalf("rerun failed: %#v", result)
	}
	executions := eng.List()
	for _, summary := range executions {
		if !summary.Status.Terminal() {
			if err := eng.Cancel(summary.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestResourceSubscriptionsAndCatalogChanges(t *testing.T) {
	var mu sync.Mutex
	pipelines := `[{"name":"demo","description":"First"}]`
	status := "running"
	fail := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if fail {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		} else if r.URL.Path == "/api/pipelines" {
			fmt.Fprint(w, pipelines)
		} else {
			fmt.Fprintf(w, `{"id":"one","status":%q}`, status)
		}
	}))
	defer api.Close()
	s, err := New(context.Background(), Options{URL: api.URL, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	updated := make(chan string, 10)
	listChanged := make(chan bool, 10)
	session := connect(t, s, &mcp.ClientOptions{
		ResourceUpdatedHandler: func(_ context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
			updated <- req.Params.URI
		},
		ResourceListChangedHandler: func(context.Context, *mcp.ResourceListChangedRequest) {
			listChanged <- true
		},
	})
	if err := session.Subscribe(context.Background(), &mcp.SubscribeParams{URI: "signalbox://executions/one"}); err != nil {
		t.Fatal(err)
	}
	waitSubscription := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			s.mu.Lock()
			found := false
			for _, resources := range s.subscriptions {
				_, found = resources["signalbox://executions/one"]
				if found {
					break
				}
			}
			s.mu.Unlock()
			if found == want {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("subscription state did not become %v", want)
	}
	waitSubscription(true)
	if err := s.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-updated:
		t.Fatal("unchanged resource produced notification")
	default:
	}
	mu.Lock()
	status = "completed"
	pipelines = `[{"name":"new","description":"Second"}]`
	mu.Unlock()
	if err := s.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case uri := <-updated:
		if uri != "signalbox://executions/one" {
			t.Fatalf("updated URI = %q", uri)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no resource update notification")
	}
	select {
	case <-listChanged:
	case <-time.After(3 * time.Second):
		t.Fatal("no resource list change notification")
	}
	resources, err := session.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, resource := range resources.Resources {
		if resource.URI == "signalbox://pipelines/demo" {
			t.Fatal("deleted pipeline still listed")
		}
		found = found || resource.URI == "signalbox://pipelines/new"
	}
	if !found {
		t.Fatal("new pipeline was not listed")
	}
	mu.Lock()
	fail = true
	mu.Unlock()
	if err := s.poll(context.Background()); err == nil {
		t.Fatal("polling failure was hidden")
	}
	select {
	case <-updated:
	case <-time.After(3 * time.Second):
		t.Fatal("resource becoming unavailable did not notify")
	}
	if err := session.Unsubscribe(context.Background(), &mcp.UnsubscribeParams{URI: "signalbox://executions/one"}); err != nil {
		t.Fatal(err)
	}
	waitSubscription(false)
	mu.Lock()
	fail = false
	status = "running"
	mu.Unlock()
	if err := s.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-updated:
		t.Fatal("unsubscribed resource produced notification")
	default:
	}
}

func TestErrorsBoundsFilesAndDefaultTools(t *testing.T) {
	var multipartSeen atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/pipelines":
			fmt.Fprint(w, `[{"name":"demo"}]`)
		case "/api/schemas":
			fmt.Fprint(w, `{"schemas":[{"name":"input","schema":{"type":"object"}}]}`)
		case "/api/executions":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				http.Error(w, "invalid multipart", 400)
				return
			}
			defer r.MultipartForm.RemoveAll()
			file, header, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				http.Error(w, "missing file", 400)
				return
			}
			file.Close()
			multipartSeen.Store(header.Filename == "notes.txt" && r.FormValue("pipeline") == "demo")
			fmt.Fprint(w, `{"id":"created"}`)
		case "/api/executions/one/restart":
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"error":"definition changed","definition_changed":true,"changes":["node changed"]}`)
		case "/api/executions/one":
			fmt.Fprintf(w, `{"id":"one","status":"running","runs":[{"id":"session","output":"a日本b","session":{"round":1,"max_rounds":2,"turns":[{"round":1,"participant":"a","output":"hello"},{"round":1,"participant":"b","output":"world"}]}}]}`)
		case "/api/executions/big":
			fmt.Fprintf(w, `{"input":%q}`, strings.Repeat("x", maxResourceBytes))
		case "/api/executions/one/files/notes.txt":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "notes")
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"not found"}`)
		}
	}))
	defer api.Close()
	s, err := New(context.Background(), Options{URL: api.URL, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	session := connect(t, s, nil)
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 8 {
		t.Fatalf("default tools = %d", len(tools.Tools))
	}
	for _, tool := range tools.Tools {
		if tool.Name == "resolve_approval" || tool.Name == "submit_manual_edit" {
			t.Fatal("human gate tools exposed by default")
		}
	}
	conflict := call(t, session, "restart_execution", map[string]any{"execution_id": "one"})
	if !conflict.IsError || !strings.Contains(conflict.Content[0].(*mcp.TextContent).Text, `"definition_changed":true`) {
		t.Fatalf("conflict details lost: %#v", conflict)
	}
	result := call(t, session, "start_execution", map[string]any{
		"pipeline": "demo", "input": "", "files": []map[string]string{
			{"name": "notes.txt", "content_base64": base64.StdEncoding.EncodeToString([]byte("notes"))},
		},
	})
	if result.IsError || !multipartSeen.Load() {
		t.Fatalf("file start = %#v, multipart seen = %v", result, multipartSeen.Load())
	}
	for _, uri := range []string{
		"signalbox://executions/missing",
		"signalbox://executions/big",
		"signalbox://executions?page_size=11",
		"signalbox://executions/one/runs/session/output?offset=-1",
		"signalbox://executions/one/runs/session/output?limit=65537",
		"signalbox://executions/one/runs/session/transcript?limit=101",
		"signalbox://executions/one/runs/session/output?offset=99",
		"signalbox://executions/one?unexpected=true",
		"signalbox://executions/%2e%2e",
		"signalbox://executions/a%2fb",
		"signalbox://executions/one?offset=0&offset=1",
	} {
		t.Run(uri, func(t *testing.T) {
			if _, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri}); err == nil {
				t.Fatalf("invalid or oversized resource accepted: %s", uri)
			}
		})
	}
	outputURI := "signalbox://executions/one/runs/session/output?offset=1&limit=2"
	if got := read(t, session, outputURI); !strings.Contains(got, `"output":"日本"`) || !strings.Contains(got, `"next_offset":3`) {
		t.Fatalf("Unicode output page = %s", got)
	}
	if got := read(t, session, "signalbox://executions/one/runs/session/transcript?offset=1&limit=1"); !strings.Contains(got, `"output":"world"`) || strings.Contains(got, `"output":"hello"`) {
		t.Fatalf("transcript page = %s", got)
	}
	if got := read(t, session, "signalbox://schemas/input"); !strings.Contains(got, `"type":"object"`) {
		t.Fatalf("schema = %s", got)
	}
	file, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "signalbox://executions/one/files/notes.txt"})
	if err != nil || len(file.Contents) != 1 || string(file.Contents[0].Blob) != "notes" {
		t.Fatalf("file resource = %#v, %v", file, err)
	}
}

func TestRejectInvalidServerURLs(t *testing.T) {
	for _, target := range []string{"", "file:///tmp", "http://user:pass@localhost", "http://localhost/api", "http://localhost?x=1", "http://localhost#fragment"} {
		if _, err := New(context.Background(), Options{URL: target}); err == nil {
			t.Fatalf("accepted invalid server URL %q", target)
		}
	}

}

func TestExactByteLimits(t *testing.T) {
	var responseSize atomic.Int64
	var starts atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/pipelines":
			fmt.Fprint(w, "[]")
		case r.Method == http.MethodPost:
			starts.Add(1)
			if _, err := io.Copy(io.Discard, r.Body); err != nil {
				t.Error(err)
			}
			fmt.Fprint(w, `{"id":"created"}`)
		default:
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write(bytes.Repeat([]byte("x"), int(responseSize.Load())))
		}
	}))
	defer api.Close()
	s, err := New(context.Background(), Options{URL: api.URL, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	responseSize.Store(maxResourceBytes)
	result, err := s.readURI(context.Background(), "signalbox://executions/one/files/data.bin")
	if err != nil || len(result.Contents[0].Blob) != maxResourceBytes {
		t.Fatalf("exact resource limit failed: %v", err)
	}
	responseSize.Store(maxResourceBytes + 1)
	if _, err := s.readURI(context.Background(), "signalbox://executions/one/files/data.bin"); err == nil {
		t.Fatal("accepted a resource one byte above the limit")
	}
	responseSize.Store(maxAPIBytes)
	if data, _, err := s.request(context.Background(), http.MethodGet, "/large", nil); err != nil || len(data) != maxAPIBytes {
		t.Fatalf("exact upstream limit failed: length %d, %v", len(data), err)
	}
	responseSize.Store(maxAPIBytes + 1)
	if _, _, err := s.request(context.Background(), http.MethodGet, "/large", nil); err == nil {
		t.Fatal("accepted an upstream response one byte above the limit")
	}
	for _, test := range []struct {
		size int
		ok   bool
	}{{8 << 20, true}, {(8 << 20) + 1, false}} {
		in := startInput{Pipeline: "demo", Files: []fileInput{{
			Name: "data.bin", ContentBase64: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("x"), test.size)),
		}}}
		_, err := s.startWithFiles(context.Background(), in)
		if (err == nil) != test.ok {
			t.Fatalf("attachment size %d: error %v, want success %v", test.size, err, test.ok)
		}
	}
	if starts.Load() != 1 {
		t.Fatalf("oversized attachment reached API: %d start requests", starts.Load())
	}
}

func TestCommandAPIMappings(t *testing.T) {
	type recorded struct {
		Method string
		Path   string
		Body   map[string]any
	}
	requests := make(chan recorded, 20)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/pipelines" {
			fmt.Fprint(w, "[]")
			return
		}
		entry := recorded{Method: r.Method, Path: r.URL.Path}
		if r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&entry.Body); err != nil {
				t.Error(err)
				http.Error(w, "bad body", 400)
				return
			}
		}
		requests <- entry
		if strings.HasSuffix(r.URL.Path, "/restart") || strings.HasSuffix(r.URL.Path, "/rerun") {
			fmt.Fprint(w, `{"id":"new"}`)
		} else {
			fmt.Fprint(w, `{"status":"ok"}`)
		}
	}))
	defer api.Close()
	s, err := New(context.Background(), Options{URL: api.URL, Version: "test", AllowReview: true})
	if err != nil {
		t.Fatal(err)
	}
	session := connect(t, s, nil)
	for _, test := range []struct {
		name   string
		args   map[string]any
		method string
		path   string
		body   string
	}{
		{"cancel_execution", map[string]any{"execution_id": "one"}, "POST", "/cancel", "null"},
		{"rerun_execution", map[string]any{"execution_id": "one"}, "POST", "/rerun", "null"},
		{"retry_run", map[string]any{"execution_id": "one", "run_id": "child-run"}, "POST", "/retry", `{"run_id":"child-run"}`},
		{"restart_execution", map[string]any{"execution_id": "one", "run_id": "r", "stage": "s"}, "POST", "/restart", `{"run_id":"r","node_id":"","stage":"s","force":false}`},
		{"restart_execution", map[string]any{"execution_id": "one", "node_id": "n", "force": true}, "POST", "/restart", `{"run_id":"","node_id":"n","stage":"","force":true}`},
		{"add_chunk", map[string]any{"execution_id": "one", "title": "Task"}, "POST", "/chunks", `{"title":"Task","by":"mcp-client"}`},
		{"complete_chunk", map[string]any{"execution_id": "one", "chunk_id": "C1"}, "POST", "/chunks/C1/done", `{"by":"mcp-client"}`},
		{"remove_chunk", map[string]any{"execution_id": "one", "chunk_id": "C1"}, "DELETE", "/chunks/C1", "null"},
		{"resolve_approval", map[string]any{"execution_id": "one", "run_id": "r", "approved": false, "comment": "revise"}, "POST", "/approve", `{"run_id":"r","approved":false,"comment":"revise","approved_by":"mcp-client"}`},
		{"submit_manual_edit", map[string]any{"execution_id": "one", "run_id": "r", "output": ""}, "POST", "/manual-edit", `{"run_id":"r","output":"","edited_by":"mcp-client"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if result := call(t, session, test.name, test.args); result.IsError {
				t.Fatalf("command failed: %#v", result)
			}
			select {
			case entry := <-requests:
				if entry.Method != test.method || entry.Path != "/api/executions/one"+test.path {
					t.Fatalf("request = %#v", entry)
				}
				got, err := json.Marshal(entry.Body)
				if err != nil {
					t.Fatal(err)
				}
				var want any
				if err := json.Unmarshal([]byte(test.body), &want); err != nil {
					t.Fatal(err)
				}
				wantJSON, err := json.Marshal(want)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != string(wantJSON) {
					t.Fatalf("body = %s, want %s", got, wantJSON)
				}
			default:
				t.Fatal("command did not call API")
			}
		})
	}
	for _, test := range []struct {
		name string
		args map[string]any
	}{
		{"cancel_execution", map[string]any{"execution_id": "../settings"}},
		{"complete_chunk", map[string]any{"execution_id": "one", "chunk_id": ".."}},
		{"remove_chunk", map[string]any{"execution_id": "", "chunk_id": "C1"}},
		{"start_execution", map[string]any{"pipeline": "", "input": "text"}},
		{"start_execution", map[string]any{"pipeline": "demo", "input": "", "files": []map[string]string{{"name": "a.txt", "content_base64": "not base64"}}}},
		{"start_execution", map[string]any{"pipeline": "demo", "input": "", "files": []map[string]string{{"name": "../a.txt", "content_base64": ""}}}},
	} {
		result := call(t, session, test.name, test.args)
		if !result.IsError {
			t.Fatalf("invalid input accepted by %s: %#v", test.name, test.args)
		}
		select {
		case request := <-requests:
			t.Fatalf("invalid input reached API: %#v", request)
		default:
		}
	}
}
