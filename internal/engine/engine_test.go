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

func TestAdditionalInputsAppendPriorNodeOutput(t *testing.T) {
	var judgeInput string
	eng := newTestEngine(t, `
name: p
entry: requirements_loader
agents:
  - id: requirements_loader
    runtime: requirements
    handoff: [coord]
  - id: coord
    type: orchestrator
    stages:
      - id: planning
        agents: [planner_a, planner_b]
  - id: planner_a
    runtime: planner_a
    handoff: [judge]
  - id: planner_b
    runtime: planner_b
    handoff: [judge]
  - id: judge
    runtime: judge
    additional-input: [requirements_loader]
`, map[string]func(ctx context.Context, req runtime.Request) (string, error){
		"requirements": func(_ context.Context, _ runtime.Request) (string, error) { return "requirements output", nil },
		"planner_a":    func(_ context.Context, _ runtime.Request) (string, error) { return "plan a", nil },
		"planner_b":    func(_ context.Context, _ runtime.Request) (string, error) { return "plan b", nil },
		"judge": func(_ context.Context, req runtime.Request) (string, error) {
			judgeInput = req.Input
			return "judged", nil
		},
	})
	id, err := eng.Start("p", "story")
	if err != nil {
		t.Fatal(err)
	}
	exec := waitDone(t, eng, id)
	if exec.Status != StatusCompleted {
		t.Fatalf("status = %s (%s)", exec.Status, exec.Error)
	}
	if !strings.Contains(judgeInput, "--- handoff from planner_a ---") || !strings.Contains(judgeInput, "--- handoff from planner_b ---") {
		t.Fatalf("judge input missing planner handoffs: %q", judgeInput)
	}
	if !strings.Contains(judgeInput, "--- additional input from requirements_loader ---\nrequirements output") {
		t.Fatalf("judge input missing requirements output: %q", judgeInput)
	}
}

func TestAdditionalInputWaitsForSameStageOutput(t *testing.T) {
	var dependentInput string
	sourceCalls := 0
	eng := newTestEngine(t, `
name: p
entry: coord
agents:
  - id: coord
    type: orchestrator
    stages:
      - id: warmup
        agents: [source]
      - id: work
        agents: [dependent, source]
  - id: source
    runtime: source
  - id: dependent
    runtime: dependent
    additional-input: [source]
`, map[string]func(ctx context.Context, req runtime.Request) (string, error){
		"source": func(_ context.Context, _ runtime.Request) (string, error) {
			sourceCalls++
			return fmt.Sprintf("source output %d", sourceCalls), nil
		},
		"dependent": func(_ context.Context, req runtime.Request) (string, error) {
			dependentInput = req.Input
			return "done", nil
		},
	})
	id, err := eng.Start("p", "stage input")
	if err != nil {
		t.Fatal(err)
	}
	exec := waitDone(t, eng, id)
	if exec.Status != StatusCompleted {
		t.Fatalf("status = %s (%s)", exec.Status, exec.Error)
	}
	if !strings.Contains(dependentInput, "source output 1") || !strings.Contains(dependentInput, "--- additional input from source ---\nsource output 2") {
		t.Fatalf("dependent input = %q", dependentInput)
	}
}

func TestAdditionalInputWaitsForUndispatchedSource(t *testing.T) {
	releaseSource := make(chan struct{})
	consumerInput := make(chan string, 1)
	eng := newTestEngine(t, `
name: p
entry: start
agents:
  - id: start
    runtime: start
    handoff: [early, late]
  - id: early
    runtime: early
    handoff: [consumer]
  - id: late
    runtime: late
    handoff: [source]
  - id: source
    runtime: source
  - id: consumer
    runtime: consumer
    additional-input: [source]
`, map[string]func(ctx context.Context, req runtime.Request) (string, error){
		"start": func(_ context.Context, _ runtime.Request) (string, error) { return "start", nil },
		"early": func(_ context.Context, _ runtime.Request) (string, error) { return "early result", nil },
		"late": func(ctx context.Context, _ runtime.Request) (string, error) {
			select {
			case <-releaseSource:
				return "late result", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
		"source": func(_ context.Context, _ runtime.Request) (string, error) { return "delayed source output", nil },
		"consumer": func(_ context.Context, req runtime.Request) (string, error) {
			consumerInput <- req.Input
			return "consumed", nil
		},
	})
	id, err := eng.Start("p", "task")
	if err != nil {
		t.Fatal(err)
	}
	close(releaseSource)
	exec := waitDone(t, eng, id)
	if exec.Status != StatusCompleted {
		t.Fatalf("status = %s (%s)", exec.Status, exec.Error)
	}
	select {
	case input := <-consumerInput:
		if !strings.Contains(input, "early result") || !strings.Contains(input, "--- additional input from source ---\ndelayed source output") {
			t.Fatalf("consumer input = %q", input)
		}
	default:
		t.Fatal("consumer was not run")
	}
}

func waitForRetry(t *testing.T, eng *Engine, id string) *NodeRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runs := eng.WaitingRetries(id); len(runs) > 0 {
			return runs[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no run became waiting_retry")
	return nil
}

func TestAgentFailureAbandonedFailsExecution(t *testing.T) {
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
	failed := waitForRetry(t, eng, id)
	if !strings.Contains(failed.Error, "kaboom") {
		t.Fatalf("run error = %q", failed.Error)
	}
	if exec, _ := eng.Get(id); exec.Status != StatusRunning {
		t.Fatalf("execution status while awaiting retry = %s, want running", exec.Status)
	}
	if got := eng.List()[0].Retrying; got != 1 {
		t.Fatalf("summary waiting_retries = %d, want 1", got)
	}
	if err := eng.ResolveFailure(id, failed.ID, false); err != nil {
		t.Fatal(err)
	}
	exec := waitDone(t, eng, id)
	if exec.Status != StatusFailed {
		t.Fatalf("status = %s", exec.Status)
	}
	if !strings.Contains(exec.Error, "kaboom") {
		t.Fatalf("error = %q", exec.Error)
	}
	if err := eng.Retry(id, failed.ID); err == nil {
		t.Fatal("retry after abandoning should fail")
	}
}

func TestRetryFailedAgentResumesChain(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	eng := newTestEngine(t, `
name: p
entry: a
agents:
  - id: a
    runtime: flaky
    handoff: [b]
  - id: b
    runtime: suffix
`, map[string]func(ctx context.Context, req runtime.Request) (string, error){
		"flaky": func(_ context.Context, req runtime.Request) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls < 3 {
				return "", fmt.Errorf("transient %d", calls)
			}
			return strings.ToUpper(req.Input), nil
		},
		"suffix": func(_ context.Context, req runtime.Request) (string, error) { return req.Input + "!", nil },
	})
	id, _ := eng.Start("p", "hello")
	for attempt := 0; attempt < 2; attempt++ {
		failed := waitForRetry(t, eng, id)
		if failed.Retries != attempt {
			t.Fatalf("retries = %d, want %d", failed.Retries, attempt)
		}
		if err := eng.Retry(id, failed.ID); err != nil {
			t.Fatal(err)
		}
	}
	exec := waitDone(t, eng, id)
	if exec.Status != StatusCompleted {
		t.Fatalf("status = %s (%s)", exec.Status, exec.Error)
	}
	if len(exec.FinalOutputs) != 1 || exec.FinalOutputs[0].Output != "HELLO!" {
		t.Fatalf("final outputs = %+v", exec.FinalOutputs)
	}
	a := exec.Runs[0]
	if a.NodeID != "a" || a.Status != StatusCompleted || a.Error != "" || a.Retries != 2 {
		t.Fatalf("run a = %+v", a)
	}
}

func TestRetryInsideStageKeepsParallelBranchesAndWaits(t *testing.T) {
	var mu sync.Mutex
	failNext := true
	eng := newTestEngine(t, `
name: p
entry: orch
agents:
  - id: orch
    type: orchestrator
    runtime: synth
    stages:
      - id: s1
        agents: [good, flaky]
        prompt: merge
    handoff: [final]
  - id: good
    runtime: mock
  - id: flaky
    runtime: flaky
  - id: final
    runtime: mock
`, map[string]func(ctx context.Context, req runtime.Request) (string, error){
		"flaky": func(_ context.Context, _ runtime.Request) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			if failNext {
				failNext = false
				return "", fmt.Errorf("flaky down")
			}
			return "flaky ok", nil
		},
		"synth": func(_ context.Context, req runtime.Request) (string, error) { return "SYNTH[" + req.Input + "]", nil },
	})
	id, _ := eng.Start("p", "go")
	failed := waitForRetry(t, eng, id)
	if failed.NodeID != "flaky" || failed.StageID != "s1" {
		t.Fatalf("failed run = %+v", failed)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		exec, _ := eng.Get(id)
		var goodDone bool
		for _, r := range exec.Runs {
			if r.NodeID == "good" && r.Status == StatusCompleted {
				goodDone = true
			}
			if r.NodeID == "final" {
				t.Fatal("pipeline advanced past a stage with an unresolved failure")
			}
		}
		if goodDone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("parallel branch did not complete while failure awaited retry")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := eng.Retry(id, failed.ID); err != nil {
		t.Fatal(err)
	}
	exec := waitDone(t, eng, id)
	if exec.Status != StatusCompleted {
		t.Fatalf("status = %s (%s)", exec.Status, exec.Error)
	}
	if !strings.Contains(exec.FinalOutputs[0].Output, "flaky ok") {
		t.Fatalf("final output missing retried result: %q", exec.FinalOutputs[0].Output)
	}
}

func TestRetryOrchestratorSynthesis(t *testing.T) {
	var mu sync.Mutex
	failNext := true
	eng := newTestEngine(t, `
name: p
entry: orch
agents:
  - id: orch
    type: orchestrator
    runtime: synth
    stages:
      - id: s1
        agents: [w]
        prompt: merge
  - id: w
    runtime: mock
`, map[string]func(ctx context.Context, req runtime.Request) (string, error){
		"synth": func(_ context.Context, req runtime.Request) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			if failNext {
				failNext = false
				return "", fmt.Errorf("synth down")
			}
			return "SYNTH", nil
		},
	})
	id, _ := eng.Start("p", "go")
	failed := waitForRetry(t, eng, id)
	if failed.NodeID != "orch" {
		t.Fatalf("failed run = %+v", failed)
	}
	if err := eng.Retry(id, failed.ID); err != nil {
		t.Fatal(err)
	}
	exec := waitDone(t, eng, id)
	if exec.Status != StatusCompleted || exec.FinalOutputs[0].Output != "SYNTH" {
		t.Fatalf("exec = %s %+v (%s)", exec.Status, exec.FinalOutputs, exec.Error)
	}
	if exec.Runs[0].Status != StatusCompleted || exec.Runs[0].Retries != 1 {
		t.Fatalf("orchestrator run = %+v", exec.Runs[0])
	}
}

func TestCancelWhileAwaitingRetry(t *testing.T) {
	eng := newTestEngine(t, `
name: p
entry: a
agents:
  - id: a
    runtime: boom
`, map[string]func(ctx context.Context, req runtime.Request) (string, error){
		"boom": func(_ context.Context, _ runtime.Request) (string, error) { return "", fmt.Errorf("kaboom") },
	})
	id, _ := eng.Start("p", "x")
	failed := waitForRetry(t, eng, id)
	if err := eng.Cancel(id); err != nil {
		t.Fatal(err)
	}
	exec := waitDone(t, eng, id)
	if exec.Status != StatusCanceled || exec.Runs[0].Status != StatusCanceled {
		t.Fatalf("exec %s, run %s", exec.Status, exec.Runs[0].Status)
	}
	if err := eng.Retry(id, failed.ID); err == nil {
		t.Fatal("retry after cancel should fail")
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

func TestStandardAgentJoinsInsideStage(t *testing.T) {
	var mu sync.Mutex
	judgeInputs := []string{}
	eng := newTestEngine(t, `
name: p
entry: coord
agents:
  - id: coord
    type: orchestrator
    stages:
      - id: plan
        agents: [pa, pb, solo]
  - id: pa
    runtime: echo
    handoff: [judge]
  - id: pb
    runtime: echo
    handoff: [judge]
  - id: solo
    runtime: echo
  - id: judge
    runtime: judge
`, map[string]func(ctx context.Context, req runtime.Request) (string, error){
		"echo": func(_ context.Context, req runtime.Request) (string, error) { return "planned", nil },
		"judge": func(_ context.Context, req runtime.Request) (string, error) {
			mu.Lock()
			judgeInputs = append(judgeInputs, req.Input)
			mu.Unlock()
			return "judged", nil
		},
	})
	id, _ := eng.Start("p", "story")
	exec := waitDone(t, eng, id)
	if exec.Status != StatusCompleted {
		t.Fatalf("status = %s (%s)", exec.Status, exec.Error)
	}
	if len(judgeInputs) != 1 {
		t.Fatalf("judge ran %d times, want 1", len(judgeInputs))
	}
	if !strings.Contains(judgeInputs[0], "--- handoff from pa ---") || !strings.Contains(judgeInputs[0], "--- handoff from pb ---") {
		t.Fatalf("judge input missing a planner: %q", judgeInputs[0])
	}
	stage := exec.Runs[0].Stages[0]
	if len(stage.Outputs) != 2 {
		t.Fatalf("stage outputs = %+v, want judge and solo", stage.Outputs)
	}
	for _, r := range exec.Runs {
		if r.NodeID == "judge" && r.FromNodeID != "pa, pb" && r.FromNodeID != "pb, pa" {
			t.Fatalf("judge from = %q", r.FromNodeID)
		}
	}
}

func TestDiamondJoinsAtTopLevel(t *testing.T) {
	var mu sync.Mutex
	runs := 0
	eng := newTestEngine(t, `
name: p
entry: a
agents:
  - id: a
    runtime: mock
    handoff: [b, c]
  - id: b
    runtime: mock
    handoff: [d]
  - id: c
    runtime: mock
    handoff: [d]
  - id: d
    runtime: count
`, map[string]func(ctx context.Context, req runtime.Request) (string, error){
		"count": func(_ context.Context, req runtime.Request) (string, error) {
			mu.Lock()
			runs++
			mu.Unlock()
			return "done", nil
		},
	})
	id, _ := eng.Start("p", "x")
	exec := waitDone(t, eng, id)
	if exec.Status != StatusCompleted {
		t.Fatalf("status = %s (%s)", exec.Status, exec.Error)
	}
	if runs != 1 || len(exec.FinalOutputs) != 1 {
		t.Fatalf("d ran %d times with %d final outputs, want 1 and 1", runs, len(exec.FinalOutputs))
	}
}
