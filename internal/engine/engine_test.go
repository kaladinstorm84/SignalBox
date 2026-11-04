package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gitea/signalbox/internal/config"
	"github.com/gitea/signalbox/internal/pipeline"
	"github.com/gitea/signalbox/internal/runtime"
)

func newTestEngine(t *testing.T, pipelineYAML string, handlers map[string]func(ctx context.Context, req runtime.Request) (string, error)) *Engine {
	t.Helper()
	p, err := pipeline.Parse([]byte(pipelineYAML))
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	for name, h := range handlers {
		registry.Register(&runtime.Mock{RuntimeName: name, Handler: h})
	}
	eng, err := New(map[string]*pipeline.Pipeline{p.Name: p}, registry, nil)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	return eng
}

func waitDone(t *testing.T, eng *Engine, id string) *Execution {
	t.Helper()
	done, err := eng.Wait(id)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("execution did not finish within 10s")
	}
	exec, err := eng.Get(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return exec
}

func TestLinearChain(t *testing.T) {
	eng := newTestEngine(t, `
name: p
entry: a
agents:
  - id: a
    runtime: upper
    handoff: [b]
  - id: b
    runtime: suffix
`, map[string]func(ctx context.Context, req runtime.Request) (string, error){
		"upper":  func(_ context.Context, req runtime.Request) (string, error) { return strings.ToUpper(req.Input), nil },
		"suffix": func(_ context.Context, req runtime.Request) (string, error) { return req.Input + "!", nil },
	})
	id, err := eng.Start("p", "hello")
	if err != nil {
		t.Fatal(err)
	}
	exec := waitDone(t, eng, id)
	if exec.Status != StatusCompleted {
		t.Fatalf("status = %s (%s)", exec.Status, exec.Error)
	}
	if len(exec.FinalOutputs) != 1 || exec.FinalOutputs[0].Output != "HELLO!" {
		t.Fatalf("final outputs = %+v", exec.FinalOutputs)
	}
}

func TestFanOutProducesParallelBranches(t *testing.T) {
	eng := newTestEngine(t, `
name: p
entry: a
agents:
  - id: a
    runtime: mock
    handoff: [b, c]
  - id: b
    runtime: mock
  - id: c
    runtime: mock
`, nil)
	id, _ := eng.Start("p", "x")
	exec := waitDone(t, eng, id)
	if exec.Status != StatusCompleted {
		t.Fatalf("status = %s (%s)", exec.Status, exec.Error)
	}
	if len(exec.FinalOutputs) != 2 {
		t.Fatalf("expected 2 final outputs, got %d", len(exec.FinalOutputs))
	}
}

func TestApprovalFlow(t *testing.T) {
	eng := newTestEngine(t, `
name: p
entry: a
agents:
  - id: a
    runtime: mock
    handoff: [gate]
  - id: gate
    type: approval
    handoff: [b]
  - id: b
    runtime: mock
`, nil)
	id, _ := eng.Start("p", "payload")

	// Wait for the gate to become pending.
	var waiting []*NodeRun
	deadline := time.Now().Add(5 * time.Second)
	for len(waiting) == 0 && time.Now().Before(deadline) {
		waiting = eng.WaitingApprovals(id)
		time.Sleep(10 * time.Millisecond)
	}
	if len(waiting) != 1 {
		t.Fatalf("expected one waiting approval, got %d", len(waiting))
	}
	if err := eng.Approve(id, waiting[0].ID, true, "tester", "looks good"); err != nil {
		t.Fatal(err)
	}
	exec := waitDone(t, eng, id)
	if exec.Status != StatusCompleted {
		t.Fatalf("status = %s (%s)", exec.Status, exec.Error)
	}
	var gate *NodeRun
	for _, r := range exec.Runs {
		if r.NodeID == "gate" {
			gate = r
		}
	}
	if gate == nil || gate.Status != StatusCompleted || gate.ApprovedBy != "tester" || gate.Comment != "looks good" {
		t.Fatalf("gate run = %+v", gate)
	}
	if len(exec.FinalOutputs) != 1 {
		t.Fatalf("final outputs = %+v", exec.FinalOutputs)
	}
}

func TestApprovalRejection(t *testing.T) {
	eng := newTestEngine(t, `
name: p
entry: gate
agents:
  - id: gate
    type: approval
    handoff: [b]
  - id: b
    runtime: mock
`, nil)
	id, _ := eng.Start("p", "payload")
	deadline := time.Now().Add(5 * time.Second)
	var waiting []*NodeRun
	for len(waiting) == 0 && time.Now().Before(deadline) {
		waiting = eng.WaitingApprovals(id)
		time.Sleep(10 * time.Millisecond)
	}
	if err := eng.Approve(id, waiting[0].ID, false, "tester", "nope"); err != nil {
		t.Fatal(err)
	}
	exec := waitDone(t, eng, id)
	if exec.Status != StatusRejected {
		t.Fatalf("status = %s", exec.Status)
	}
	for _, r := range exec.Runs {
		if r.NodeID == "b" {
			t.Fatal("agent b should never have run after rejection")
		}
	}
}

func TestOrchestratorFanInAndStages(t *testing.T) {
	var mu sync.Mutex
	var order []string
	record := func(name string) func(ctx context.Context, req runtime.Request) (string, error) {
		return func(_ context.Context, req runtime.Request) (string, error) {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return name + "(" + req.Input + ")", nil
		}
	}
	eng := newTestEngine(t, `
name: p
entry: start
agents:
  - id: start
    runtime: mock
    handoff: [left, right]
  - id: left
    runtime: left
    handoff: [orch]
  - id: right
    runtime: right
    handoff: [orch]
  - id: orch
    type: orchestrator
    runtime: synth
    stages:
      - id: s1
        agents: [w1, w2]
        prompt: merge stage one
      - id: s2
        agents: [w3]
    handoff: [final]
  - id: w1
    runtime: w1
  - id: w2
    runtime: w2
  - id: w3
    runtime: w3
  - id: final
    runtime: final
`, map[string]func(ctx context.Context, req runtime.Request) (string, error){
		"left":  func(_ context.Context, req runtime.Request) (string, error) { return "L", nil },
		"right": func(_ context.Context, req runtime.Request) (string, error) { return "R", nil },
		"w1":    record("w1"),
		"w2":    record("w2"),
		"w3":    record("w3"),
		"synth": func(_ context.Context, req runtime.Request) (string, error) { return "SYNTH[" + req.Input + "]", nil },
		"final": func(_ context.Context, req runtime.Request) (string, error) { return "FINAL:" + req.Input, nil },
	})
	id, _ := eng.Start("p", "go")
	exec := waitDone(t, eng, id)
	if exec.Status != StatusCompleted {
		t.Fatalf("status = %s (%s)", exec.Status, exec.Error)
	}

	var orch *NodeRun
	for _, r := range exec.Runs {
		if r.NodeID == "orch" {
			orch = r
		}
	}
	if orch == nil {
		t.Fatal("no orchestrator run recorded")
	}
	// Fan-in: the orchestrator input must contain both upstream results.
	if !strings.Contains(orch.Input, "L") || !strings.Contains(orch.Input, "R") {
		t.Fatalf("orchestrator input missing fan-in payloads: %q", orch.Input)
	}
	if len(orch.Stages) != 2 {
		t.Fatalf("stages = %d, want 2", len(orch.Stages))
	}
	s1, s2 := orch.Stages[0], orch.Stages[1]
	if s1.Status != StatusCompleted || s2.Status != StatusCompleted {
		t.Fatalf("stage statuses: %s, %s", s1.Status, s2.Status)
	}
	if len(s1.Outputs) != 2 {
		t.Fatalf("stage s1 collected %d outputs, want 2", len(s1.Outputs))
	}
	// Stage 1 context must be the synthesis result.
	if !strings.HasPrefix(s1.Context, "SYNTH[") {
		t.Fatalf("stage s1 context = %q", s1.Context)
	}
	// w3 must run after both w1 and w2 (stage ordering).
	if len(order) != 3 || order[2] != "w3" {
		t.Fatalf("agent order = %v, want w3 last", order)
	}
	// The final agent receives the orchestrator's output.
	if len(exec.FinalOutputs) != 1 || !strings.HasPrefix(exec.FinalOutputs[0].Output, "FINAL:") {
		t.Fatalf("final outputs = %+v", exec.FinalOutputs)
	}
}

func TestAgentFailureFailsExecution(t *testing.T) {
	eng := newTestEngine(t, `
name: p
entry: a
agents:
  - id: a
    runtime: boom
    handoff: [b]
  - id: b
    runtime: mock
`, map[string]func(ctx context.Context, req runtime.Request) (string, error){
		"boom": func(_ context.Context, _ runtime.Request) (string, error) { return "", fmt.Errorf("kaboom") },
	})
	id, _ := eng.Start("p", "x")
	exec := waitDone(t, eng, id)
	if exec.Status != StatusFailed {
		t.Fatalf("status = %s", exec.Status)
	}
	if !strings.Contains(exec.Error, "kaboom") {
		t.Fatalf("error = %q", exec.Error)
	}
}

func TestCancelExecution(t *testing.T) {
	block := make(chan struct{})
	eng := newTestEngine(t, `
name: p
entry: a
agents:
  - id: a
    runtime: slow
`, map[string]func(ctx context.Context, req runtime.Request) (string, error){
		"slow": func(ctx context.Context, _ runtime.Request) (string, error) {
			select {
			case <-block:
				return "done", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	})
	id, _ := eng.Start("p", "x")
	time.Sleep(50 * time.Millisecond)
	if err := eng.Cancel(id); err != nil {
		t.Fatal(err)
	}
	exec := waitDone(t, eng, id)
	if exec.Status != StatusCanceled {
		t.Fatalf("status = %s", exec.Status)
	}
	close(block)
}

func TestStoreRoundTripAndInterrupted(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	e := &Execution{
		ID:        "20260101-000000-aaaa",
		Pipeline:  "p",
		Input:     "x",
		Status:    StatusRunning,
		CreatedAt: now,
		Runs: []*NodeRun{
			{ID: "r1", NodeID: "a", Type: pipeline.TypeStandard, Status: StatusRunning},
		},
	}
	if err := store.Save(e); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d executions", len(loaded))
	}
	got := loaded[0]
	if got.Status != StatusInterrupted {
		t.Fatalf("status = %s, want interrupted", got.Status)
	}
	if got.Runs[0].Status != StatusInterrupted {
		t.Fatalf("run status = %s, want interrupted", got.Runs[0].Status)
	}
}
