package engine

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

const paramsPipeline = `name: p
params:
  audience:
    type: string
    values: [engineers, executives]
    default: engineers
  topic:
    required: true
  model:
    default: ""
entry: a
agents:
  - id: a
    runtime: w
    model: ${{ params.model }}
    prompt: Explain ${{ params.topic }} for ${{ params.audience }}.
`

func TestStartWithParamsSubstitutes(t *testing.T) {
	var mu sync.Mutex
	var reqs []runtime.Request
	eng := newTestEngine(t, paramsPipeline, map[string]func(context.Context, runtime.Request) (string, error){
		"w": func(_ context.Context, req runtime.Request) (string, error) {
			mu.Lock()
			reqs = append(reqs, req)
			mu.Unlock()
			return "ok", nil
		},
	})
	id, err := eng.StartWithParams("p", "in", map[string]string{"topic": "queues", "model": "gpt-x"})
	if err != nil {
		t.Fatal(err)
	}
	execution := waitDone(t, eng, id)
	if execution.Status != StatusCompleted {
		t.Fatalf("status = %s (%s)", execution.Status, execution.Error)
	}
	if len(reqs) != 1 || reqs[0].SystemPrompt != "Explain queues for engineers." || reqs[0].Model != "gpt-x" {
		t.Fatalf("requests = %+v", reqs)
	}
	want := map[string]string{"audience": "engineers", "topic": "queues", "model": "gpt-x"}
	for k, v := range want {
		if execution.Params[k] != v {
			t.Fatalf("params = %v", execution.Params)
		}
	}
	if run := findRun(t, execution, "a"); run.Model != "gpt-x" {
		t.Fatalf("run model = %q", run.Model)
	}
	// The registered pipeline keeps its placeholders.
	if p := eng.pipelines["p"]; !strings.Contains(p.Node("a").Prompt, "${{ params.topic }}") {
		t.Fatalf("template mutated: %q", p.Node("a").Prompt)
	}
}

func TestStartWithParamsRejectsInvalid(t *testing.T) {
	eng := newTestEngine(t, paramsPipeline, map[string]func(context.Context, runtime.Request) (string, error){
		"w": func(context.Context, runtime.Request) (string, error) { return "ok", nil },
	})
	if _, err := eng.Start("p", "in"); err == nil || !strings.Contains(err.Error(), "topic") {
		t.Fatalf("missing required: %v", err)
	}
	if _, err := eng.StartWithParams("p", "in", map[string]string{"topic": "x", "audience": "kids"}); err == nil {
		t.Fatal("expected enum error")
	}
	if _, err := eng.StartWithParams("p", "in", map[string]string{"topic": "x", "bogus": "1"}); err == nil {
		t.Fatal("expected unknown param error")
	}
}

func TestParamsReusedByRestart(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := pipeline.Parse([]byte(paramsPipeline))
	if err != nil {
		t.Fatal(err)
	}
	previous := &Execution{ID: "20261105-000000-dddd", Pipeline: "p", Input: "in", Status: StatusRunning, CreatedAt: time.Now().UTC(),
		Params: map[string]string{"topic": "caches", "audience": "executives", "model": ""},
		Runs:   []*NodeRun{{ID: "a-run", NodeID: "a", Type: pipeline.TypeStandard, Status: StatusRunning, Input: "in"}}}
	if err := store.Save(previous); err != nil {
		t.Fatal(err)
	}
	stateDir := store.stateDir
	store.Close()
	store, err = NewStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	var got runtime.Request
	registry.Register(&runtime.Mock{RuntimeName: "w", Handler: func(_ context.Context, req runtime.Request) (string, error) {
		got = req
		return "done", nil
	}})
	eng, err := New(map[string]*pipeline.Pipeline{"p": p}, registry, store)
	if err != nil {
		t.Fatal(err)
	}
	newID, err := eng.RestartFromInterrupted(previous.ID, "a-run")
	if err != nil {
		t.Fatal(err)
	}
	execution := waitDone(t, eng, newID)
	if execution.Status != StatusCompleted || got.SystemPrompt != "Explain caches for executives." || execution.Params["topic"] != "caches" {
		t.Fatalf("status %s, prompt %q, params %v", execution.Status, got.SystemPrompt, execution.Params)
	}
}

func mcpEngine(t *testing.T, yaml string, mcp config.MCPConfig, handlers map[string]func(context.Context, runtime.Request) (string, error)) *Engine {
	t.Helper()
	eng := newTestEngine(t, yaml, handlers)
	eng.registry.SetMCP(mcp)
	return eng
}

var testMCP = config.MCPConfig{Enabled: true, Servers: map[string]config.MCPServerConfig{
	"github": {Command: "gh-mcp", Tools: []string{"search_issues", "get_issue"}, EnvFrom: map[string]string{"TOKEN": "SECRET_TOKEN_ENV"}},
	"docs":   {Command: "docs-mcp", Tools: []string{"search"}},
}}

func TestMCPGrantsRecordedAndPassed(t *testing.T) {
	var got runtime.Request
	eng := mcpEngine(t, `name: p
entry: a
agents:
  - id: a
    runtime: w
    mcp:
      - server: github
        tools: [search_issues]
      - docs
`, testMCP, map[string]func(context.Context, runtime.Request) (string, error){
		"w": func(_ context.Context, req runtime.Request) (string, error) { got = req; return "ok", nil },
	})
	id, err := eng.Start("p", "in")
	if err != nil {
		t.Fatal(err)
	}
	execution := waitDone(t, eng, id)
	if execution.Status != StatusCompleted {
		t.Fatalf("status = %s (%s)", execution.Status, execution.Error)
	}
	want := []runtime.MCPGrant{{Agent: "a", Server: "github", Tools: []string{"search_issues"}}, {Agent: "a", Server: "docs", Tools: []string{"search"}}}
	gotJSON, _ := json.Marshal(got.MCP)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("request grants = %s", gotJSON)
	}
	run := findRun(t, execution, "a")
	runJSON, _ := json.Marshal(run)
	if !strings.Contains(string(runJSON), `"server":"github"`) || strings.Contains(string(runJSON), "SECRET_TOKEN_ENV") {
		t.Fatalf("run json = %s", runJSON)
	}
}

func TestMCPGrantFailsWhenDisabled(t *testing.T) {
	eng := mcpEngine(t, "name: p\nentry: a\nagents:\n  - id: a\n    runtime: w\n    mcp: [docs]\n", config.MCPConfig{},
		map[string]func(context.Context, runtime.Request) (string, error){
			"w": func(context.Context, runtime.Request) (string, error) {
				t.Error("runtime should not be invoked")
				return "", nil
			},
		})
	id, err := eng.Start("p", "in")
	if err != nil {
		t.Fatal(err)
	}
	execution := waitDone(t, eng, id)
	if execution.Status != StatusFailed || !strings.Contains(execution.Error+findRun(t, execution, "a").Error, "mcp.enabled") {
		t.Fatalf("status %s, error %q / %q", execution.Status, execution.Error, findRun(t, execution, "a").Error)
	}
}

func TestSessionTurnsUseParticipantModelAndGrants(t *testing.T) {
	var mu sync.Mutex
	got := map[string]runtime.Request{}
	h := func(name string) func(context.Context, runtime.Request) (string, error) {
		return func(_ context.Context, req runtime.Request) (string, error) {
			mu.Lock()
			got[name] = req
			mu.Unlock()
			return "VOTE: AGREE", nil
		}
	}
	eng := mcpEngine(t, `name: p
entry: s
agents:
  - id: s
    type: session
    participants: [x, y]
    max_rounds: 1
  - id: x
    runtime: x
    model: small
    mcp: [docs]
  - id: y
    runtime: y
`, testMCP, map[string]func(context.Context, runtime.Request) (string, error){"x": h("x"), "y": h("y")})
	id, err := eng.Start("p", "in")
	if err != nil {
		t.Fatal(err)
	}
	execution := waitDone(t, eng, id)
	if execution.Status != StatusCompleted {
		t.Fatalf("status = %s (%s)", execution.Status, execution.Error)
	}
	if got["x"].Model != "small" || len(got["x"].MCP) != 1 || got["x"].MCP[0].Server != "docs" {
		t.Fatalf("x request = %+v", got["x"])
	}
	if got["y"].Model != "" || len(got["y"].MCP) != 0 {
		t.Fatalf("y request = %+v", got["y"])
	}
	if run := findRun(t, execution, "s"); len(run.MCP) != 1 || run.MCP[0].Agent != "x" {
		t.Fatalf("session grants = %+v", run.MCP)
	}
}
