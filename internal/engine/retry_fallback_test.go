package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

func TestSchemaRepairStartsFreshFallbackSequence(t *testing.T) {
	var mu sync.Mutex
	var sequence []string
	fallbackCalls := 0
	eng := newSchemaEngine(t, `name: p
agents:
  - id: a
    runtime: {provider: first}
    retry:
      attempts:
        - runtime: {provider: backup}
    output: {schema: triage}
`, "", map[string]func(context.Context, runtime.Request) (string, error){
		"first": func(_ context.Context, req runtime.Request) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			sequence = append(sequence, "first")
			if req.Schema == nil {
				t.Error("lost output schema on initial runtime")
			}
			return "", fmt.Errorf("unavailable")
		},
		"backup": func(_ context.Context, req runtime.Request) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			sequence = append(sequence, "backup")
			fallbackCalls++
			if req.Schema == nil {
				t.Error("lost output schema on fallback")
			}
			if fallbackCalls == 1 {
				return "not JSON", nil
			}
			return `{"risk":"low","total":1}`, nil
		},
	})
	id, err := eng.Start("p", "task")
	if err != nil {
		t.Fatal(err)
	}
	execution := waitDone(t, eng, id)
	if execution.Status != StatusCompleted {
		t.Fatalf("execution = %+v", execution)
	}
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(sequence) != "[first backup first backup]" {
		t.Fatalf("sequence = %v", sequence)
	}
	run := execution.Runs[0]
	if len(run.SchemaAttempts) != 2 || len(run.CallAttempts) != 4 || run.Retries != 2 {
		t.Fatalf("run = %+v", run)
	}
}

func TestFallbackRetriesUseIndependentSettingsAndPersist(t *testing.T) {
	t.Setenv("SIGNALBOX_RETRY_TEST_KEY", "test-key")
	type call struct {
		Path   string
		Model  string `json:"model"`
		Effort string `json:"reasoning_effort"`
		Tier   string `json:"service_tier"`
	}
	var mu sync.Mutex
	var calls []call
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got call
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		got.Path = r.URL.Path
		mu.Lock()
		calls = append(calls, got)
		n := len(calls)
		mu.Unlock()
		if n <= 3 {
			http.Error(w, `{"error":{"message":"temporary failure"}}`, http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"recovered"}}]}`)
	}))
	defer api.Close()
	p, err := pipeline.Parse([]byte(`name: p
agents:
  - id: a
    runtime:
      provider: first
      model: node-model
      effort: medium
      speed: standard
    retry:
      backoff: 1ms
      attempts:
        - runtime:
            model: retry-model
            effort: high
            speed: fast
        - runtime: {provider: backup}
`))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Runtimes: map[string]config.RuntimeConfig{
		"first":  {Type: "openai", BaseURL: api.URL + "/first", Model: "first-default", APIKeyEnv: "SIGNALBOX_RETRY_TEST_KEY", ReasoningEffort: "low", Speed: "fast"},
		"backup": {Type: "openai", BaseURL: api.URL + "/backup", Model: "backup-default", APIKeyEnv: "SIGNALBOX_RETRY_TEST_KEY", ReasoningEffort: "high", Speed: "fast"},
	}}
	if err := runtime.ValidatePipeline(cfg, p); err != nil {
		t.Fatal(err)
	}
	reg, err := runtime.NewRegistryForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng, err := New(map[string]*pipeline.Pipeline{"p": p}, reg, store)
	if err != nil {
		t.Fatal(err)
	}
	id, err := eng.Start("p", "unchanged input")
	if err != nil {
		t.Fatal(err)
	}
	failed := waitForRetry(t, eng, id)
	if failed.Retries != 2 || len(failed.CallAttempts) != 3 {
		t.Fatalf("waiting run = %+v", failed)
	}
	if err := eng.Retry(id, failed.ID); err != nil {
		t.Fatal(err)
	}
	execution := waitDone(t, eng, id)
	if execution.Status != StatusCompleted {
		t.Fatalf("execution = %s: %s", execution.Status, execution.Error)
	}
	mu.Lock()
	got := append([]call(nil), calls...)
	mu.Unlock()
	want := []call{
		{"/first/chat/completions", "node-model", "medium", "default"},
		{"/first/chat/completions", "retry-model", "high", "priority"},
		{"/backup/chat/completions", "node-model", "medium", "default"},
		{"/backup/chat/completions", "node-model", "medium", "default"},
	}
	if len(got) != len(want) {
		t.Fatalf("calls = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	persisted, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	run := persisted.Runs[0]
	if run.Retries != 3 || len(run.CallAttempts) != 4 || run.Output != "recovered" || run.Input != "unchanged input" {
		t.Fatalf("persisted run = %+v", run)
	}
	for i, attempt := range run.CallAttempts {
		if attempt.StartedAt.IsZero() || attempt.FinishedAt == nil || (i < 3 && attempt.Error == "") || (i == 3 && attempt.Error != "") {
			t.Errorf("attempt %d = %+v", i, attempt)
		}
	}
	if p.Node("a").Model != "node-model" || p.Node("a").Speed != "standard" {
		t.Fatal("retry mutated the pipeline")
	}
}

func TestFallbackRetriesAcrossAICallTypes(t *testing.T) {
	t.Setenv("SIGNALBOX_RETRY_TEST_KEY", "test-key")
	for _, tc := range []struct {
		name, yaml, output string
		calls              int
	}{
		{"standard", `agents:
  - id: a
    runtime: first
    retry: {attempts: [{runtime: {provider: backup}}]}
`, "done", 2},
		{"gate", `agents:
  - id: a
    type: llm_gate
    runtime: first
    prompt: Review the input.
    retry: {attempts: [{runtime: {provider: backup}}]}
`, `{"decision":"approve","feedback":"ok"}`, 2},
		{"session", `agents:
  - id: session
    type: session
    participants: [a, b]
    summary_agent: a
    max_rounds: 1
    retry: {attempts: [{runtime: {provider: backup}}]}
  - id: a
    runtime: first
  - id: b
    runtime: first
`, "VOTE: AGREE", 6},
		{"orchestrator", `agents:
  - id: coordinator
    type: orchestrator
    runtime: first
    prompt: Final summary.
    retry: {attempts: [{runtime: {provider: backup}}]}
    stages:
      - id: work
        agents: [a]
        prompt: Stage summary.
  - id: a
    runtime: mock
`, "done", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var calls []string
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				calls = append(calls, r.URL.Path)
				mu.Unlock()
				if r.URL.Path == "/first/chat/completions" {
					http.Error(w, `{"error":{"message":"unavailable"}}`, http.StatusServiceUnavailable)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": tc.output}}}})
			}))
			defer api.Close()
			p, err := pipeline.Parse([]byte("name: p\n" + tc.yaml))
			if err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{Runtimes: map[string]config.RuntimeConfig{
				"first":  {Type: "openai", BaseURL: api.URL + "/first", Model: "initial", APIKeyEnv: "SIGNALBOX_RETRY_TEST_KEY", ReasoningEffort: "medium", Speed: "standard"},
				"backup": {Type: "openai", BaseURL: api.URL + "/backup", Model: "fallback", APIKeyEnv: "SIGNALBOX_RETRY_TEST_KEY", ReasoningEffort: "high", Speed: "fast"},
				"mock":   {Type: "mock"},
			}}
			if err := runtime.ValidatePipeline(cfg, p); err != nil {
				t.Fatal(err)
			}
			reg, err := runtime.NewRegistryForConfig(cfg)
			if err != nil {
				t.Fatal(err)
			}
			eng, err := New(map[string]*pipeline.Pipeline{"p": p}, reg, nil)
			if err != nil {
				t.Fatal(err)
			}
			id, err := eng.Start("p", "task")
			if err != nil {
				t.Fatal(err)
			}
			execution := waitDone(t, eng, id)
			if execution.Status != StatusCompleted {
				t.Fatalf("status = %s: %s", execution.Status, execution.Error)
			}
			mu.Lock()
			got := append([]string(nil), calls...)
			mu.Unlock()
			if len(got) != tc.calls {
				t.Fatalf("calls = %v, want %d", got, tc.calls)
			}
			for i, path := range got {
				want := "/first/chat/completions"
				if i%2 == 1 {
					want = "/backup/chat/completions"
				}
				if path != want {
					t.Errorf("call %d = %s, want %s", i, path, want)
				}
			}
			run := execution.Runs[0]
			if len(run.CallAttempts) != tc.calls {
				t.Fatalf("history = %+v", run.CallAttempts)
			}
			for i, attempt := range run.CallAttempts {
				if i%2 == 0 && (attempt.Model != "initial" || attempt.ReasoningEffort != "medium" || attempt.Speed != "standard") {
					t.Errorf("initial defaults = %+v", attempt)
				}
				if i%2 == 1 && (attempt.Model != "fallback" || attempt.ReasoningEffort != "high" || attempt.Speed != "fast") {
					t.Errorf("fallback defaults = %+v", attempt)
				}
			}
		})
	}
}
