package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

// callCounter records calls per runtime and can fail chosen calls.
type callCounter struct {
	mu     sync.Mutex
	calls  map[string]int
	inputs map[string][]string
	fail   map[string]int // runtime -> fail calls up to this number
}

func newCallCounter() *callCounter {
	return &callCounter{calls: map[string]int{}, inputs: map[string][]string{}, fail: map[string]int{}}
}

func (c *callCounter) handler(name string) func(context.Context, runtime.Request) (string, error) {
	return func(_ context.Context, req runtime.Request) (string, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.calls[name]++
		c.inputs[name] = append(c.inputs[name], req.Input)
		if c.calls[name] <= c.fail[name] {
			return "", fmt.Errorf("%s failed (call %d)", name, c.calls[name])
		}
		return name + " output", nil
	}
}

func (c *callCounter) count(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[name]
}

func (c *callCounter) lastInput(name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	inputs := c.inputs[name]
	if len(inputs) == 0 {
		return ""
	}
	return inputs[len(inputs)-1]
}

func newRecoveryEngine(t *testing.T, yaml string, counter *callCounter, names ...string) (*Engine, map[string]*pipeline.Pipeline) {
	t.Helper()
	p, err := pipeline.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		registry.Register(&runtime.Mock{RuntimeName: name, Handler: counter.handler(name)})
	}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	pipelines := map[string]*pipeline.Pipeline{p.Name: p}
	eng, err := New(pipelines, registry, store)
	if err != nil {
		t.Fatal(err)
	}
	return eng, pipelines
}

// failExecution waits for a run to park after a failure, then gives up on it.
func failExecution(t *testing.T, eng *Engine, id string) *Execution {
	t.Helper()
	failed := waitForRetry(t, eng, id)
	if err := eng.ResolveFailure(id, failed.ID, false); err != nil {
		t.Fatal(err)
	}
	execution := waitDone(t, eng, id)
	if execution.Status != StatusFailed {
		t.Fatalf("source status = %s, want failed", execution.Status)
	}
	return execution
}

func freshAndReused(e *Execution) (fresh []string, reused []string) {
	for _, run := range e.Runs {
		if run.ReusedFrom == "" {
			fresh = append(fresh, run.NodeID)
		} else {
			reused = append(reused, run.NodeID)
		}
	}
	return fresh, reused
}

const fanInYAML = `
name: fan
entry: start
agents:
  - id: start
    runtime: start
    handoff: [left, right]
  - id: left
    runtime: left
    handoff: [join]
  - id: right
    runtime: right
    handoff: [join]
  - id: join
    runtime: join
    handoff: [publish]
  - id: publish
    runtime: publish
`

func TestRestartFromFailedRunReusesCompletedBranches(t *testing.T) {
	counter := newCallCounter()
	counter.fail["right"] = 1
	eng, _ := newRecoveryEngine(t, fanInYAML, counter, "start", "left", "right", "join", "publish", "audit")
	id, err := eng.Start("fan", "task")
	if err != nil {
		t.Fatal(err)
	}
	source := failExecution(t, eng, id)
	if source.Definition == nil || source.Definition.Hash == "" || len(source.Definition.Nodes) != 5 {
		t.Fatalf("definition fingerprint = %+v", source.Definition)
	}

	// No run given: the failed run is detected automatically.
	newID, err := eng.Restart(id, RestartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	restarted := waitDone(t, eng, newID)
	if restarted.Status != StatusCompleted {
		t.Fatalf("restart status = %s (%s)", restarted.Status, restarted.Error)
	}
	if counter.count("start") != 1 || counter.count("left") != 1 {
		t.Fatalf("completed work repeated: start=%d left=%d", counter.count("start"), counter.count("left"))
	}
	if counter.count("right") != 2 || counter.count("join") != 1 || counter.count("publish") != 1 {
		t.Fatalf("calls right=%d join=%d publish=%d", counter.count("right"), counter.count("join"), counter.count("publish"))
	}
	joinInput := counter.lastInput("join")
	if !strings.Contains(joinInput, "left output") || !strings.Contains(joinInput, "right output") {
		t.Fatalf("join input = %q, want both branches", joinInput)
	}
	fresh, reused := freshAndReused(restarted)
	if strings.Join(fresh, ",") != "right,join,publish" || strings.Join(reused, ",") != "start,left" {
		t.Fatalf("fresh=%v reused=%v", fresh, reused)
	}
	rec := restarted.Recovery
	if rec == nil || rec.SourceExecution != id || rec.NodeID != "right" || rec.ReusedRuns != 2 || rec.Forced || len(rec.Warnings) != 0 {
		t.Fatalf("recovery = %+v", rec)
	}
	if len(restarted.FinalOutputs) != 1 || restarted.FinalOutputs[0].Output != "publish output" {
		t.Fatalf("final outputs = %+v", restarted.FinalOutputs)
	}

	// The record survives a reload from the store.
	eng.mu.Lock()
	delete(eng.execs, newID)
	eng.mu.Unlock()
	stored, err := eng.Get(newID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Recovery == nil || stored.Recovery.SourceRun != rec.SourceRun || stored.Definition == nil {
		t.Fatalf("stored recovery = %+v definition = %+v", stored.Recovery, stored.Definition)
	}
}

func TestRestartFromCompletedRunRerunsOnlyDownstream(t *testing.T) {
	counter := newCallCounter()
	eng, _ := newRecoveryEngine(t, fanInYAML, counter, "start", "left", "right", "join", "publish", "audit")
	id, err := eng.Start("fan", "task")
	if err != nil {
		t.Fatal(err)
	}
	source := waitDone(t, eng, id)
	if source.Status != StatusCompleted {
		t.Fatalf("status = %s", source.Status)
	}
	if _, err := eng.Restart(id, RestartOptions{}); err == nil || !strings.Contains(err.Error(), "no failed") {
		t.Fatalf("auto restart of a completed execution: err = %v", err)
	}

	newID, err := eng.Restart(id, RestartOptions{NodeID: "left"})
	if err != nil {
		t.Fatal(err)
	}
	restarted := waitDone(t, eng, newID)
	if restarted.Status != StatusCompleted {
		t.Fatalf("restart status = %s (%s)", restarted.Status, restarted.Error)
	}
	if counter.count("start") != 1 || counter.count("right") != 1 || counter.count("left") != 2 || counter.count("join") != 2 {
		t.Fatalf("calls = %+v", counter.calls)
	}
	if input := counter.lastInput("join"); !strings.Contains(input, "right output") || !strings.Contains(input, "left output") {
		t.Fatalf("join input = %q", input)
	}
	if counter.lastInput("left") != "start output" {
		t.Fatalf("left re-run input = %q, want its saved input", counter.lastInput("left"))
	}
}

func TestRestartDetectsChangedDefinition(t *testing.T) {
	counter := newCallCounter()
	counter.fail["right"] = 1
	eng, pipelines := newRecoveryEngine(t, fanInYAML, counter, "start", "left", "right", "join", "publish", "audit")
	id, err := eng.Start("fan", "task")
	if err != nil {
		t.Fatal(err)
	}
	failExecution(t, eng, id)

	changed, err := pipeline.Parse([]byte(strings.Replace(fanInYAML, "runtime: join\n", "runtime: join\n    prompt: judge carefully\n", 1)))
	if err != nil {
		t.Fatal(err)
	}
	pipelines["fan"] = changed
	eng.SetPipelines(pipelines)

	_, err = eng.Restart(id, RestartOptions{})
	var changedErr *DefinitionChangedError
	if !errors.As(err, &changedErr) || len(changedErr.Changes) != 1 || changedErr.Changes[0] != "modified agents: join" {
		t.Fatalf("err = %v", err)
	}
	if counter.count("right") != 1 {
		t.Fatal("refused restart still ran work")
	}

	newID, err := eng.Restart(id, RestartOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	restarted := waitDone(t, eng, newID)
	if restarted.Status != StatusCompleted || !restarted.Recovery.Forced {
		t.Fatalf("forced restart = %s recovery %+v", restarted.Status, restarted.Recovery)
	}
	if len(restarted.Recovery.Warnings) != 1 || !strings.Contains(restarted.Recovery.Warnings[0], "modified agents: join") {
		t.Fatalf("warnings = %v", restarted.Recovery.Warnings)
	}
	if restarted.Definition.Hash == "" || restarted.Definition.Nodes["join"] == "" {
		t.Fatal("restarted execution has no fingerprint")
	}

	// A removed restart node is an error even with force.
	removed, err := pipeline.Parse([]byte(strings.Replace(strings.Replace(fanInYAML, "    handoff: [left, right]\n", "    handoff: [left]\n", 1),
		"  - id: right\n    runtime: right\n    handoff: [join]\n", "", 1)))
	if err != nil {
		t.Fatal(err)
	}
	pipelines["fan"] = removed
	eng.SetPipelines(pipelines)
	if _, err := eng.Restart(id, RestartOptions{Force: true}); err == nil || !strings.Contains(err.Error(), `agent "right" is no longer in pipeline`) {
		t.Fatalf("removed node err = %v", err)
	}
}

func TestRestartOrchestratorFromStage(t *testing.T) {
	counter := newCallCounter()
	counter.fail["review"] = 1
	eng, _ := newRecoveryEngine(t, `
name: staged
entry: lead
agents:
  - id: lead
    type: orchestrator
    runtime: synth
    stages:
      - id: research
        agents: [research]
        prompt: summarise research
      - id: review
        agents: [review]
      - id: ship
        agents: [ship]
  - id: research
    runtime: research
  - id: review
    runtime: review
  - id: ship
    runtime: ship
`, counter, "synth", "research", "review", "ship")
	id, err := eng.Start("staged", "task")
	if err != nil {
		t.Fatal(err)
	}
	source := failExecution(t, eng, id)
	runID, stage, err := RestartPoint(source)
	if err != nil || stage != "review" || runID != source.Runs[0].ID {
		t.Fatalf("restart point = %q %q %v", runID, stage, err)
	}
	if _, err := eng.Restart(id, RestartOptions{RunID: source.Runs[2].ID}); err == nil || !strings.Contains(err.Error(), "inside stage") {
		t.Fatalf("stage run restart err = %v", err)
	}
	if _, err := eng.Restart(id, RestartOptions{RunID: runID, Stage: "ship"}); err == nil || !strings.Contains(err.Error(), `earlier stage "review" did not complete`) {
		t.Fatalf("unreached stage err = %v", err)
	}

	newID, err := eng.Restart(id, RestartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	restarted := waitDone(t, eng, newID)
	if restarted.Status != StatusCompleted {
		t.Fatalf("restart status = %s (%s)", restarted.Status, restarted.Error)
	}
	if counter.count("research") != 1 || counter.count("synth") != 1 || counter.count("review") != 2 || counter.count("ship") != 1 {
		t.Fatalf("calls = %+v", counter.calls)
	}
	if counter.lastInput("review") != "synth output" {
		t.Fatalf("review input = %q, want the saved research stage context", counter.lastInput("review"))
	}
	var orch *NodeRun
	for _, run := range restarted.Runs {
		if run.NodeID == "lead" {
			orch = run
		}
		if run.NodeID == "research" && (run.ReusedFrom != id || run.OrchRunID == "" || run.OrchRunID == source.Runs[0].ID) {
			t.Fatalf("reused stage run = %+v", run)
		}
	}
	if orch == nil || len(orch.Stages) != 3 || orch.Stages[0].Context != "synth output" || orch.Stages[2].Status != StatusCompleted {
		t.Fatalf("orchestrator run = %+v", orch)
	}
	if restarted.Recovery.Stage != "review" || restarted.Recovery.ReusedRuns != 1 {
		t.Fatalf("recovery = %+v", restarted.Recovery)
	}
}

func TestRestartRestoresChunkSnapshot(t *testing.T) {
	var mu sync.Mutex
	current := ""
	var seen [][]Chunk
	p, err := pipeline.Parse([]byte(`
name: chunky
entry: plan
agents:
  - id: plan
    runtime: mock
    handoff: [check]
  - id: check
    type: approval
    handoff: [build]
  - id: build
    runtime: build
`))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	var eng *Engine
	registry.Register(&runtime.Mock{RuntimeName: "build", Handler: func(_ context.Context, _ runtime.Request) (string, error) {
		mu.Lock()
		id := current
		mu.Unlock()
		chunks, _ := eng.ListChunks(id)
		mu.Lock()
		seen = append(seen, chunks)
		calls := len(seen)
		mu.Unlock()
		if calls == 1 {
			_, _ = eng.AddChunk(id, "added during build", "build")
			return "", fmt.Errorf("build broke")
		}
		return "built", nil
	}})
	eng, err = New(map[string]*pipeline.Pipeline{"chunky": p}, registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := eng.Start("chunky", "task")
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	current = id
	mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for len(eng.WaitingApprovals(id)) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := eng.AddChunk(id, "before build", "person"); err != nil {
		t.Fatal(err)
	}
	if err := eng.Approve(id, eng.WaitingApprovals(id)[0].ID, true, "person", ""); err != nil {
		t.Fatal(err)
	}
	source := failExecution(t, eng, id)
	if len(source.Chunks) != 2 {
		t.Fatalf("source chunks = %+v", source.Chunks)
	}

	newID, err := func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		newID, err := eng.Restart(id, RestartOptions{})
		current = newID
		return newID, err
	}()
	if err != nil {
		t.Fatal(err)
	}
	restarted := waitDone(t, eng, newID)
	if restarted.Status != StatusCompleted {
		t.Fatalf("restart status = %s (%s)", restarted.Status, restarted.Error)
	}
	if len(seen) != 2 || len(seen[1]) != 1 || seen[1][0].Title != "before build" {
		t.Fatalf("chunks seen by restarted build = %+v", seen)
	}
	if len(restarted.Chunks) != 1 || restarted.NextChunk != 1 {
		t.Fatalf("restarted chunks = %+v next %d", restarted.Chunks, restarted.NextChunk)
	}
	// Restarting from the approval asks again rather than reusing the decision.
	againID, err := eng.Restart(id, RestartOptions{NodeID: "check"})
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for len(eng.WaitingApprovals(againID)) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(eng.WaitingApprovals(againID)) != 1 {
		t.Fatal("restarted approval is not waiting")
	}
	if err := eng.Cancel(againID); err != nil {
		t.Fatal(err)
	}
}

func TestRestartRequiresFinishedExecution(t *testing.T) {
	eng := newTestEngine(t, "name: p\nentry: gate\nagents:\n  - id: gate\n    type: approval\n", nil)
	id, err := eng.Start("p", "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Restart(id, RestartOptions{}); err == nil || !strings.Contains(err.Error(), "cancel it before restarting") {
		t.Fatalf("err = %v", err)
	}
	if err := eng.Cancel(id); err != nil {
		t.Fatal(err)
	}
	waitDone(t, eng, id)
	if _, err := eng.Restart(id, RestartOptions{RunID: "missing"}); err == nil || !strings.Contains(err.Error(), "unknown run") {
		t.Fatalf("err = %v", err)
	}
}

func TestDefinitionChangesListing(t *testing.T) {
	a, _ := pipeline.Parse([]byte("name: p\nentry: x\nagents:\n  - id: x\n    runtime: mock\n    handoff: [y]\n  - id: y\n    runtime: mock\n"))
	b, _ := pipeline.Parse([]byte("name: p\nentry: x\nagents:\n  - id: x\n    runtime: mock\n    handoff: [z]\n  - id: z\n    runtime: mock\n"))
	if changes := definitionChanges(fingerprint(a), fingerprint(a)); changes != nil {
		t.Fatalf("identical definitions differ: %v", changes)
	}
	got := strings.Join(definitionChanges(fingerprint(a), fingerprint(b)), "; ")
	if got != "modified agents: x; removed agents: y; added agents: z" {
		t.Fatalf("changes = %q", got)
	}
}
