package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
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

func TestCodeStepReceivesDataRetriesAndHandsOffInsideStage(t *testing.T) {
	if _, err := exec.LookPath("python"); err != nil {
		t.Skip("Python is required for this integration test")
	}
	root := t.TempDir()
	source := filepath.Join(root, "verify.py")
	if err := os.WriteFile(source, []byte("import sys\nsys.stderr.write('Validation failed')\nsys.exit(1)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := pipeline.Parse([]byte("name: p\nentry: coordinator\nagents:\n  - id: coordinator\n    type: orchestrator\n    stages:\n      - id: validation\n        agents: [verify]\n  - id: verify\n    type: code\n    language: python\n    code-source: verify.py\n    timeout_seconds: 5\n    handoff: [consumer]\n  - id: consumer\n    runtime: consumer\n"))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	var consumerInput string
	registry.Register(&runtime.Mock{RuntimeName: "consumer", Handler: func(_ context.Context, req runtime.Request) (string, error) {
		consumerInput = req.Input
		return "validated", nil
	}})
	eng, err := NewWithLimit(map[string]*pipeline.Pipeline{"p": p}, registry, nil, 1, root)
	if err != nil {
		t.Fatal(err)
	}
	id, err := eng.Start("p", `{"message":"story data","items":[1,2]}`)
	if err != nil {
		t.Fatal(err)
	}
	failed := waitForRetry(t, eng, id)
	if failed.NodeID != "verify" || failed.StageID != "validation" || !strings.Contains(failed.Error, "Validation failed") {
		t.Fatalf("failed code run = %+v", failed)
	}
	code := "import json, sys\nfrom pathlib import Path\npayload = json.load(sys.stdin)\npayload['verified'] = True\npayload['root_file_found'] = Path('verify.py').is_file()\nprint(json.dumps(payload))\n"
	if err := os.WriteFile(source, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := eng.Retry(id, failed.ID); err != nil {
		t.Fatal(err)
	}
	execution := waitDone(t, eng, id)
	if execution.Status != StatusCompleted {
		t.Fatalf("status = %s (%s)", execution.Status, execution.Error)
	}
	var output struct {
		Message       string `json:"message"`
		Verified      bool   `json:"verified"`
		RootFileFound bool   `json:"root_file_found"`
	}
	if err := json.Unmarshal([]byte(consumerInput), &output); err != nil {
		t.Fatal(err)
	}
	if output.Message != "story data" || !output.Verified || !output.RootFileFound {
		t.Fatalf("consumer data = %+v", output)
	}
}

func TestCodeExitMappings(t *testing.T) {
	if _, err := exec.LookPath("python"); err != nil {
		t.Skip("Python is required for this integration test")
	}
	cases := []struct {
		name              string
		code              string
		want              Status
		workerCalls       int
		withoutCheckpoint bool
	}{
		{name: "nonzero success", code: "print('verified')\nraise SystemExit(7)\n", want: StatusCompleted, workerCalls: 1},
		{name: "terminal fail", code: "import sys\nprint('Invalid test data', file=sys.stderr)\nraise SystemExit(1)\n", want: StatusFailed, workerCalls: 1},
		{name: "rework retry", code: "import sys\ntext = sys.stdin.read()\nif 'fix requirement' not in text:\n    print('Needs corrections')\n    print('fix requirement', file=sys.stderr)\n    raise SystemExit(2)\nprint('verified')\nraise SystemExit(7)\n", want: StatusCompleted, workerCalls: 2},
	}
	fallback := cases[2]
	fallback.name = "rework retry without checkpoint"
	fallback.withoutCheckpoint = true
	cases = append(cases, fallback)
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "verify.py"), []byte(test.code), 0o600); err != nil {
				t.Fatal(err)
			}
			definition := "name: p\nentry: coordinator\nagents:\n  - id: coordinator\n    type: orchestrator\n    stages:\n      - id: validation\n        agents: [checkpoint]\n  - id: checkpoint\n    type: approval\n    handoff: [worker]\n  - id: worker\n    runtime: worker\n    handoff: [verify]\n  - id: verify\n    type: code\n    language: python\n    code-source: verify.py\n    codes:\n      success: 7\n      fail: 1\n      retry: 2\n    handoff: [consumer]\n  - id: consumer\n    runtime: consumer\n"
			if test.withoutCheckpoint {
				definition = strings.Replace(definition, "agents: [checkpoint]", "agents: [worker]", 1)
				definition = strings.Replace(definition, "  - id: checkpoint\n    type: approval\n    handoff: [worker]\n", "", 1)
			}
			p, err := pipeline.Parse([]byte(definition))
			if err != nil {
				t.Fatal(err)
			}
			registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
			if err != nil {
				t.Fatal(err)
			}
			var inputs []string
			registry.Register(&runtime.Mock{RuntimeName: "worker", Handler: func(_ context.Context, req runtime.Request) (string, error) {
				inputs = append(inputs, req.Input)
				return req.Input, nil
			}})
			consumerCalls := 0
			registry.Register(&runtime.Mock{RuntimeName: "consumer", Handler: func(_ context.Context, req runtime.Request) (string, error) {
				consumerCalls++
				return req.Input, nil
			}})
			eng, err := NewWithLimit(map[string]*pipeline.Pipeline{"p": p}, registry, nil, 1, root)
			if err != nil {
				t.Fatal(err)
			}
			id, err := eng.Start("p", "original draft")
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			approved := test.withoutCheckpoint
			for !approved && time.Now().Before(deadline) {
				waiting := eng.WaitingApprovals(id)
				if len(waiting) > 0 {
					if err := eng.Approve(id, waiting[0].ID, true, "tester", ""); err != nil {
						t.Fatal(err)
					}
					approved = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !approved {
				t.Fatal("checkpoint never became available")
			}
			execution := waitDone(t, eng, id)
			if execution.Status != test.want || len(inputs) != test.workerCalls {
				t.Fatalf("status/calls = %s/%d, want %s/%d (%s)", execution.Status, len(inputs), test.want, test.workerCalls, execution.Error)
			}
			if test.want == StatusFailed {
				if consumerCalls != 0 || !strings.Contains(execution.Error, "Invalid test data") {
					t.Fatalf("failed code continued downstream or lost its error: %d / %q", consumerCalls, execution.Error)
				}
			} else if consumerCalls != 1 {
				t.Fatalf("consumer calls = %d, want 1", consumerCalls)
			}
			if test.workerCalls == 2 {
				if !strings.Contains(inputs[1], "original draft") || !strings.Contains(inputs[1], "--- rejection notes from verify ---\nNeeds corrections\n\nfix requirement") {
					t.Fatalf("missing code rejection feedback: %q", inputs[1])
				}
				if execution.Runs[0].Stages[0].Status != StatusCompleted {
					t.Fatal("stage did not complete after code rework")
				}
			}
		})
	}
}

func TestLinearChain(t *testing.T) {
	eng := newTestEngine(t, "name: p\nentry: a\nagents:\n  - id: a\n    runtime: upper\n    handoff: [b]\n  - id: b\n    runtime: suffix\n", map[string]func(ctx context.Context, req runtime.Request) (string, error){
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

func TestRuntimeCallsWaitAtConcurrencyLimit(t *testing.T) {
	p, err := pipeline.Parse([]byte(`
name: p
entry: start
agents:
  - id: start
    runtime: seed
    handoff: [a, b, c]
  - id: a
    runtime: worker
  - id: b
    runtime: worker
  - id: c
    runtime: worker
`))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	registry.Register(&runtime.Mock{RuntimeName: "seed", Handler: func(_ context.Context, _ runtime.Request) (string, error) {
		return "seeded", nil
	}})
	started := make(chan struct{}, 3)
	release := make(chan struct{}, 3)
	var mu sync.Mutex
	active, maximum := 0, 0
	registry.Register(&runtime.Mock{RuntimeName: "worker", Handler: func(_ context.Context, _ runtime.Request) (string, error) {
		mu.Lock()
		active++
		if active > maximum {
			maximum = active
		}
		mu.Unlock()
		started <- struct{}{}
		<-release
		mu.Lock()
		active--
		mu.Unlock()
		return "done", nil
	}})
	eng, err := NewWithLimit(map[string]*pipeline.Pipeline{"p": p}, registry, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	id, err := eng.Start("p", "task")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("runtime calls did not start")
		}
	}
	select {
	case <-started:
		t.Fatal("third runtime call bypassed the concurrency limit")
	case <-time.After(50 * time.Millisecond):
	}
	release <- struct{}{}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("queued runtime call did not start after a slot became available")
	}
	release <- struct{}{}
	release <- struct{}{}
	waitDone(t, eng, id)
	mu.Lock()
	defer mu.Unlock()
	if maximum != 2 {
		t.Fatalf("maximum concurrent calls = %d, want 2", maximum)
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

func TestRejectionOnlyBranch(t *testing.T) {
	for _, inStage := range []bool{false, true} {
		for _, reject := range []bool{false, true} {
			t.Run(fmt.Sprintf("stage=%v/reject=%v", inStage, reject), func(t *testing.T) {
				entry, coordinator := "presenter", ""
				if inStage {
					entry = "coordinator"
					coordinator = "  - id: coordinator\n    type: orchestrator\n    stages:\n      - id: work\n        agents: [presenter]\n"
				}
				definition := "name: p\nentry: " + entry + "\nagents:\n" + coordinator + "  - id: presenter\n    runtime: presenter\n    handoff: [review]\n  - id: review\n    type: approval\n    reject-to: reformatter\n  - id: reformatter\n    runtime: reformatter\n    handoff: [left, right]\n  - id: left\n    runtime: left\n    handoff: [merge]\n  - id: right\n    runtime: right\n    handoff: [merge]\n  - id: merge\n    runtime: merge\n    handoff: [review]\n"
				var mu sync.Mutex
				calls := map[string][]string{}
				handlers := map[string]func(context.Context, runtime.Request) (string, error){}
				for _, name := range []string{"presenter", "reformatter", "left", "right", "merge"} {
					handlers[name] = func(_ context.Context, req runtime.Request) (string, error) {
						mu.Lock()
						defer mu.Unlock()
						calls[name] = append(calls[name], req.Input)
						return name + ": " + req.Input, nil
					}
				}
				eng := newTestEngine(t, definition, handlers)
				id, err := eng.Start("p", "initial task")
				if err != nil {
					t.Fatal(err)
				}
				waitReview := func(except string) *NodeRun {
					t.Helper()
					for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
						for _, run := range eng.WaitingApprovals(id) {
							if run.NodeID == "review" && run.ID != except {
								return run
							}
						}
						time.Sleep(10 * time.Millisecond)
					}
					t.Fatal("review was not reached")
					return nil
				}
				review := waitReview("")
				if reject {
					for _, note := range []string{"Fix the wording", "Add a heading"} {
						payload := review.Input
						if err := eng.Approve(id, review.ID, false, "tester", note); err != nil {
							t.Fatal(err)
						}
						review = waitReview(review.ID)
						mu.Lock()
						inputs := calls["reformatter"]
						input := inputs[len(inputs)-1]
						mu.Unlock()
						if input != appendRejectionNote(payload, "review", note) {
							t.Fatalf("reformatter input = %q, want current review payload and notes", input)
						}
					}
				}
				if err := eng.Approve(id, review.ID, true, "tester", "approved"); err != nil {
					t.Fatal(err)
				}
				execution := waitDone(t, eng, id)
				if execution.Status != StatusCompleted {
					t.Fatalf("status = %s (%s)", execution.Status, execution.Error)
				}
				mu.Lock()
				defer mu.Unlock()
				if len(calls["presenter"]) != 1 {
					t.Fatal("normal presenter was replayed")
				}
				want := 0
				if reject {
					want = 2
				}
				for _, name := range []string{"reformatter", "left", "right", "merge"} {
					if len(calls[name]) != want {
						t.Fatalf("%s calls = %d, want %d", name, len(calls[name]), want)
					}
				}
			})
		}
	}
}

func TestApprovalRejectToOverridesCheckpoint(t *testing.T) {
	for _, inStage := range []bool{false, true} {
		t.Run(fmt.Sprintf("stage=%v", inStage), func(t *testing.T) {
			entry, coordinator := "checkpoint", ""
			if inStage {
				entry = "coordinator"
				coordinator = "  - id: coordinator\n    type: orchestrator\n    stages:\n      - id: work\n        agents: [checkpoint]\n"
			}
			definition := "name: p\nentry: " + entry + "\nagents:\n" + coordinator + "  - id: checkpoint\n    type: approval\n    handoff: [left, right]\n  - id: left\n    runtime: left\n    handoff: [writer]\n  - id: right\n    runtime: right\n    handoff: [writer]\n  - id: writer\n    runtime: writer\n    handoff: [presenter]\n  - id: presenter\n    runtime: presenter\n    handoff: [review]\n  - id: review\n    type: approval\n    reject-to: writer\n"
			var mu sync.Mutex
			calls := map[string][]string{}
			handlers := map[string]func(context.Context, runtime.Request) (string, error){}
			for _, name := range []string{"left", "right", "writer", "presenter"} {
				handlers[name] = func(_ context.Context, req runtime.Request) (string, error) {
					mu.Lock()
					defer mu.Unlock()
					calls[name] = append(calls[name], req.Input)
					return name + " output", nil
				}
			}
			eng := newTestEngine(t, definition, handlers)
			id, err := eng.Start("p", "task")
			if err != nil {
				t.Fatal(err)
			}
			waitApproval := func(nodeID, except string) *NodeRun {
				t.Helper()
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					for _, run := range eng.WaitingApprovals(id) {
						if run.NodeID == nodeID && run.ID != except {
							return run
						}
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatalf("approval %s was not reached", nodeID)
				return nil
			}
			checkpoint := waitApproval("checkpoint", "")
			if err := eng.Approve(id, checkpoint.ID, true, "reviewer", ""); err != nil {
				t.Fatal(err)
			}
			review := waitApproval("review", "")
			for _, note := range []string{"Correct the wording", "Add missing detail"} {
				if err := eng.Approve(id, review.ID, false, "reviewer", note); err != nil {
					t.Fatal(err)
				}
				review = waitApproval("review", review.ID)
			}
			if err := eng.Approve(id, review.ID, true, "reviewer", "approved"); err != nil {
				t.Fatal(err)
			}
			execution := waitDone(t, eng, id)
			if execution.Status != StatusCompleted {
				t.Fatalf("status = %s (%s)", execution.Status, execution.Error)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(calls["left"]) != 1 || len(calls["right"]) != 1 || len(calls["writer"]) != 3 || len(calls["presenter"]) != 3 {
				t.Fatalf("wrong replayed calls: %+v", calls)
			}
			for _, text := range []string{"left output", "right output", "Correct the wording", "Add missing detail"} {
				if !strings.Contains(calls["writer"][2], text) {
					t.Fatalf("rework input missing %q: %q", text, calls["writer"][2])
				}
			}
		})
	}
}

func TestApprovalRejectionReworksFromLastApprovedGate(t *testing.T) {
	var publisherInputs []string
	eng := newTestEngine(t, `
name: multi
entry: coordinator
agents:
  - id: coordinator
    type: orchestrator
    stages:
      - id: review_stage
        agents: [triage]
  - id: triage
    runtime: triage
    handoff: [review_1]
  - id: review_1
    type: approval
    handoff: [publisher]
  - id: publisher
    runtime: publisher
    handoff: [review_2]
  - id: review_2
    type: approval
`, map[string]func(ctx context.Context, req runtime.Request) (string, error){
		"triage": func(_ context.Context, _ runtime.Request) (string, error) { return "draft from triage", nil },
		"publisher": func(_ context.Context, req runtime.Request) (string, error) {
			publisherInputs = append(publisherInputs, req.Input)
			return fmt.Sprintf("revision %d", len(publisherInputs)), nil
		},
	})
	id, err := eng.Start("multi", "task")
	if err != nil {
		t.Fatal(err)
	}
	waitForApproval := func(except string) *NodeRun {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			for _, run := range eng.WaitingApprovals(id) {
				if run.NodeID == "review_1" || run.NodeID == "review_2" {
					if run.ID != except {
						return run
					}
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("approval did not become available")
		return nil
	}
	review1 := waitForApproval("")
	if review1.NodeID != "review_1" {
		t.Fatalf("first approval = %q, want review_1", review1.NodeID)
	}
	if err := eng.Approve(id, review1.ID, true, "reviewer", "approved first draft"); err != nil {
		t.Fatal(err)
	}
	review2 := waitForApproval(review1.ID)
	if review2.NodeID != "review_2" {
		t.Fatalf("second approval = %q, want review_2", review2.NodeID)
	}
	const note = "Please correct the missing details."
	if err := eng.Approve(id, review2.ID, false, "reviewer", note); err != nil {
		t.Fatal(err)
	}
	review2Retry := waitForApproval(review2.ID)
	if review2Retry.NodeID != "review_2" {
		t.Fatalf("approval after rework = %q, want review_2", review2Retry.NodeID)
	}
	if len(publisherInputs) != 2 || publisherInputs[0] != "draft from triage" {
		t.Fatalf("publisher inputs = %q", publisherInputs)
	}
	if !strings.Contains(publisherInputs[1], "draft from triage") || !strings.Contains(publisherInputs[1], "--- rejection notes from review_2 ---\n"+note) {
		t.Fatalf("rework input = %q", publisherInputs[1])
	}
	exec, err := eng.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if exec.Status != StatusRunning {
		t.Fatalf("execution status after rejection = %s, want running", exec.Status)
	}
	if err := eng.Approve(id, review2Retry.ID, true, "reviewer", "approved revision"); err != nil {
		t.Fatal(err)
	}
	exec = waitDone(t, eng, id)
	if exec.Status != StatusCompleted {
		t.Fatalf("final status = %s (%s)", exec.Status, exec.Error)
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

func TestRestartFromInterruptedRunReusesInputAndCompletedOutputs(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := pipeline.Parse([]byte(`
name: p
entry: start
agents:
  - id: start
    runtime: mock
    handoff: [requirements_loader, interrupted_call, completed_sibling]
  - id: requirements_loader
    runtime: requirements
  - id: interrupted_call
    runtime: retry
    additional-input: [requirements_loader]
    handoff: [judge]
  - id: completed_sibling
    runtime: sibling
    handoff: [judge]
  - id: judge
    runtime: judge
`))
	if err != nil {
		t.Fatal(err)
	}
	previous := &Execution{
		ID:        "20261105-000000-aaaa",
		Pipeline:  "p",
		Input:     "original task",
		Status:    StatusRunning,
		CreatedAt: time.Now().UTC(),
		Runs: []*NodeRun{
			{ID: "start-run", NodeID: "start", Type: pipeline.TypeStandard, Status: StatusCompleted, Output: "start output"},
			{ID: "requirements-run", NodeID: "requirements_loader", Type: pipeline.TypeStandard, Status: StatusCompleted, Output: "saved requirements"},
			{ID: "sibling-run", NodeID: "completed_sibling", Type: pipeline.TypeStandard, Status: StatusCompleted, Output: "sibling output"},
			{ID: "interrupted-run", NodeID: "interrupted_call", Type: pipeline.TypeStandard, Status: StatusRunning, Input: "saved call input"},
		},
	}
	if err := store.Save(previous); err != nil {
		t.Fatal(err)
	}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	var retryInput string
	registry.Register(&runtime.Mock{RuntimeName: "retry", Handler: func(_ context.Context, req runtime.Request) (string, error) {
		retryInput = req.Input
		return "retried output", nil
	}})
	registry.Register(&runtime.Mock{RuntimeName: "sibling", Handler: func(_ context.Context, _ runtime.Request) (string, error) {
		t.Error("completed sibling was replayed")
		return "unexpected", nil
	}})
	var judgeInput string
	registry.Register(&runtime.Mock{RuntimeName: "judge", Handler: func(_ context.Context, req runtime.Request) (string, error) {
		judgeInput = req.Input
		return "judged", nil
	}})
	eng, err := New(map[string]*pipeline.Pipeline{"p": p}, registry, store)
	if err != nil {
		t.Fatal(err)
	}
	newID, err := eng.RestartFromInterrupted(previous.ID, "interrupted-run")
	if err != nil {
		t.Fatal(err)
	}
	exec := waitDone(t, eng, newID)
	if exec.Status != StatusCompleted {
		t.Fatalf("recovery status = %s (%s)", exec.Status, exec.Error)
	}
	if exec.Input != "original task" {
		t.Fatalf("recovery execution input = %q", exec.Input)
	}
	if len(exec.Runs) != 2 || exec.Runs[0].NodeID != "interrupted_call" || exec.Runs[1].NodeID != "judge" {
		t.Fatalf("recovery runs = %+v, want interrupted_call then judge", exec.Runs)
	}
	if !strings.Contains(retryInput, "saved call input") || !strings.Contains(retryInput, "--- additional input from requirements_loader ---\nsaved requirements") {
		t.Fatalf("retry input missing saved call input or requirements: %q", retryInput)
	}
	if judgeInput != "retried output" {
		t.Fatalf("judge input = %q, want retried output", judgeInput)
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
