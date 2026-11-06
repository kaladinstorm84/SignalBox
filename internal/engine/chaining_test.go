package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

// newChainEngine builds a persisting engine over several pipelines whose
// agents use the counter's runtimes.
func newChainEngine(t *testing.T, counter *callCounter, names []string, yamls ...string) *Engine {
	t.Helper()
	pipelines := map[string]*pipeline.Pipeline{}
	for _, y := range yamls {
		p, err := pipeline.Parse([]byte(y))
		if err != nil {
			t.Fatalf("pipeline: %v", err)
		}
		pipelines[p.Name] = p
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
	eng, err := New(pipelines, registry, store)
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

const childLine = `
name: child
agents:
  - id: a
    runtime: a
    prompt: a
    handoff: [b]
  - id: b
    runtime: b
    prompt: b
    handoff: [c]
  - id: c
    runtime: c
    prompt: c
    handoff: [d]
  - id: d
    runtime: d
    prompt: d
`

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPipelineNodeRunsChildAndHandsOffOutput(t *testing.T) {
	counter := newCallCounter()
	eng := newChainEngine(t, counter, []string{"prep", "a", "b", "c", "d", "finish"}, childLine, `
name: parent
agents:
  - id: prep
    runtime: prep
    prompt: prep
    handoff: [sub]
  - id: sub
    type: pipeline
    pipeline: child
    handoff: [finish]
  - id: finish
    runtime: finish
    prompt: finish
`)
	id, err := eng.Start("parent", "go")
	if err != nil {
		t.Fatal(err)
	}
	parent := waitDone(t, eng, id)
	if parent.Status != StatusCompleted {
		t.Fatalf("parent status = %s (%s)", parent.Status, parent.Error)
	}
	sub := findRun(t, parent, "sub")
	if sub.Output != "d output" || len(sub.ChildExecutions) != 1 {
		t.Fatalf("sub output %q, children %v", sub.Output, sub.ChildExecutions)
	}
	if got := counter.lastInput("finish"); got != "d output" {
		t.Fatalf("finish input = %q", got)
	}
	if got := counter.lastInput("a"); got != "prep output" {
		t.Fatalf("child entry input = %q", got)
	}
	child, err := eng.Get(sub.ChildExecutions[0])
	if err != nil {
		t.Fatal(err)
	}
	if child.Status != StatusCompleted || child.Parent == nil || child.Parent.Execution != id || child.Parent.RunID != sub.ID || child.Parent.Depth != 1 || child.Section != nil {
		t.Fatalf("child = %+v parent %+v", child.Status, child.Parent)
	}
}

func TestPipelineNodeRunsSection(t *testing.T) {
	counter := newCallCounter()
	eng := newChainEngine(t, counter, []string{"a", "b", "c", "d"}, childLine, `
name: parent
agents:
  - id: middle
    type: pipeline
    pipeline: child
    from: b
    until: c
`)
	id, err := eng.Start("parent", "start here")
	if err != nil {
		t.Fatal(err)
	}
	parent := waitDone(t, eng, id)
	if parent.Status != StatusCompleted {
		t.Fatalf("parent status = %s (%s)", parent.Status, parent.Error)
	}
	if counter.count("a") != 0 || counter.count("b") != 1 || counter.count("c") != 1 || counter.count("d") != 0 {
		t.Fatalf("calls = %v", counter.calls)
	}
	if got := counter.lastInput("b"); got != "start here" {
		t.Fatalf("section entry input = %q", got)
	}
	if len(parent.FinalOutputs) != 1 || parent.FinalOutputs[0].Output != "c output" {
		t.Fatalf("final outputs = %+v", parent.FinalOutputs)
	}
	child, err := eng.Get(findRun(t, parent, "middle").ChildExecutions[0])
	if err != nil {
		t.Fatal(err)
	}
	if child.Section == nil || child.Section.From != "b" || child.Section.Until != "c" {
		t.Fatalf("section = %+v", child.Section)
	}
	if len(child.FinalOutputs) != 1 || child.FinalOutputs[0].NodeID != "c" {
		t.Fatalf("child finals = %+v", child.FinalOutputs)
	}
}

const reviewChild = `
name: review
agents:
  - id: draft
    runtime: draft
    prompt: draft
    handoff: [signoff]
  - id: signoff
    type: approval
    handoff: [polish]
  - id: polish
    runtime: polish
    prompt: polish
`

func TestChildApprovalIsSurfacedAndHandledThroughParent(t *testing.T) {
	counter := newCallCounter()
	eng := newChainEngine(t, counter, []string{"draft", "polish"}, reviewChild, `
name: parent
agents:
  - id: review
    type: pipeline
    pipeline: review
`)
	id, err := eng.Start("parent", "x")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "surfaced approval", func() bool { return len(eng.WaitingApprovals(id)) == 1 })
	surfaced := eng.SurfacedRuns(id)
	if len(surfaced) != 1 || surfaced[0].Run.NodeID != "signoff" || surfaced[0].Pipeline != "review" {
		t.Fatalf("surfaced = %+v", surfaced)
	}
	if err := eng.Approve(id, surfaced[0].Run.ID, true, "person", "ok"); err != nil {
		t.Fatalf("approve through parent: %v", err)
	}
	parent := waitDone(t, eng, id)
	if parent.Status != StatusCompleted || findRun(t, parent, "review").Output != "polish output" {
		t.Fatalf("parent = %s %+v", parent.Status, parent.FinalOutputs)
	}
	child, err := eng.Get(surfaced[0].Execution)
	if err != nil {
		t.Fatal(err)
	}
	if run := findRun(t, child, "signoff"); run.ApprovedBy != "person" {
		t.Fatalf("approved by = %q", run.ApprovedBy)
	}
	if len(eng.SurfacedRuns(id)) != 0 {
		t.Fatal("finished execution still surfaces runs")
	}
}

func TestChildRejectionRejectsPipelineNode(t *testing.T) {
	counter := newCallCounter()
	// The section starts at the approval, so rejecting it has no upstream
	// to rework and ends the child as rejected.
	eng := newChainEngine(t, counter, []string{"draft", "polish"}, reviewChild, `
name: parent
agents:
  - id: review
    type: pipeline
    pipeline: review
    from: signoff
`)
	id, err := eng.Start("parent", "x")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "surfaced approval", func() bool { return len(eng.WaitingApprovals(id)) == 1 })
	if err := eng.Approve(id, eng.WaitingApprovals(id)[0].ID, false, "person", "no"); err != nil {
		t.Fatal(err)
	}
	parent := waitDone(t, eng, id)
	if parent.Status != StatusRejected {
		t.Fatalf("parent status = %s", parent.Status)
	}
	if run := findRun(t, parent, "review"); run.Status != StatusRejected || !strings.Contains(run.Error, "signoff") {
		t.Fatalf("review run = %s %q", run.Status, run.Error)
	}
	if counter.count("polish") != 0 {
		t.Fatal("polish ran after rejection")
	}
}

func TestFailedChildParksPipelineNodeAndRetryStartsNewChild(t *testing.T) {
	counter := newCallCounter()
	counter.fail["b"] = 1
	eng := newChainEngine(t, counter, []string{"a", "b", "c", "d"}, childLine, `
name: parent
agents:
  - id: sub
    type: pipeline
    pipeline: child
`)
	id, err := eng.Start("parent", "x")
	if err != nil {
		t.Fatal(err)
	}
	// The child's failed call surfaces on the parent; giving up fails the
	// child, which parks the pipeline node.
	waitFor(t, "child retry", func() bool { return len(eng.WaitingRetries(id)) == 1 })
	childRun := eng.WaitingRetries(id)[0]
	if childRun.NodeID != "b" {
		t.Fatalf("waiting run = %s", childRun.NodeID)
	}
	if err := eng.ResolveFailure(id, childRun.ID, false); err != nil {
		t.Fatal(err)
	}
	var subRun *NodeRun
	waitFor(t, "pipeline node retry", func() bool {
		runs := eng.WaitingRetries(id)
		if len(runs) == 1 && runs[0].NodeID == "sub" {
			subRun = runs[0]
			return true
		}
		return false
	})
	if !strings.Contains(subRun.Error, "failed") {
		t.Fatalf("sub error = %q", subRun.Error)
	}
	if err := eng.Retry(id, subRun.ID); err != nil {
		t.Fatal(err)
	}
	parent := waitDone(t, eng, id)
	if parent.Status != StatusCompleted {
		t.Fatalf("parent status = %s (%s)", parent.Status, parent.Error)
	}
	sub := findRun(t, parent, "sub")
	if len(sub.ChildExecutions) != 2 || sub.Retries != 1 {
		t.Fatalf("children %v retries %d", sub.ChildExecutions, sub.Retries)
	}
	first, err := eng.Get(sub.ChildExecutions[0])
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != StatusFailed {
		t.Fatalf("first child = %s", first.Status)
	}
}

func TestCancelParentCancelsChild(t *testing.T) {
	counter := newCallCounter()
	eng := newChainEngine(t, counter, []string{"draft", "polish"}, reviewChild, `
name: parent
agents:
  - id: review
    type: pipeline
    pipeline: review
`)
	id, err := eng.Start("parent", "x")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "surfaced approval", func() bool { return len(eng.SurfacedRuns(id)) == 1 })
	childID := eng.SurfacedRuns(id)[0].Execution
	if err := eng.Cancel(id); err != nil {
		t.Fatal(err)
	}
	if parent := waitDone(t, eng, id); parent.Status != StatusCanceled {
		t.Fatalf("parent = %s", parent.Status)
	}
	child := waitDone(t, eng, childID)
	if child.Status != StatusCanceled || !strings.Contains(child.Error, id) {
		t.Fatalf("child = %s %q", child.Status, child.Error)
	}
}

func TestPipelineNodeDepthLimit(t *testing.T) {
	counter := newCallCounter()
	// Static validation rejects reference cycles; the engine still bounds
	// nesting in case one slips through.
	eng := newChainEngine(t, counter, nil, `
name: loop
agents:
  - id: again
    type: pipeline
    pipeline: loop
`)
	id, err := eng.Start("loop", "x")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "depth failure", func() bool { return len(eng.WaitingRetries(id)) == 1 })
	if run := eng.WaitingRetries(id)[0]; !strings.Contains(run.Error, "deeper than") {
		t.Fatalf("error = %q", run.Error)
	}
	if got := len(eng.SurfacedRuns(id)); got != 1 {
		t.Fatalf("surfaced = %d", got)
	}
	if err := eng.Cancel(id); err != nil {
		t.Fatal(err)
	}
	if parent := waitDone(t, eng, id); parent.Status != StatusCanceled {
		t.Fatalf("parent = %s", parent.Status)
	}
}

func TestRestartRefusesChildExecution(t *testing.T) {
	counter := newCallCounter()
	eng := newChainEngine(t, counter, []string{"a", "b", "c", "d"}, childLine, `
name: parent
agents:
  - id: sub
    type: pipeline
    pipeline: child
`)
	id, err := eng.Start("parent", "x")
	if err != nil {
		t.Fatal(err)
	}
	parent := waitDone(t, eng, id)
	childID := findRun(t, parent, "sub").ChildExecutions[0]
	if _, err := eng.Restart(childID, RestartOptions{NodeID: "b"}); err == nil || !strings.Contains(err.Error(), "restart that execution") {
		t.Fatalf("restart child err = %v", err)
	}
	newID, err := eng.Restart(id, RestartOptions{NodeID: "sub"})
	if err != nil {
		t.Fatalf("restart parent from pipeline node: %v", err)
	}
	again := waitDone(t, eng, newID)
	if again.Status != StatusCompleted || counter.count("a") != 2 {
		t.Fatalf("restarted = %s, a calls %d", again.Status, counter.count("a"))
	}
}
