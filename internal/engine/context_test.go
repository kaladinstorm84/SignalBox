package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

// contextRuns builds a history whose FinishedAt order differs from slice
// order, so tests prove that selection is chronological.
func contextRuns() []*NodeRun {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	at := func(sec int) *time.Time { t := base.Add(time.Duration(sec) * time.Second); return &t }
	return []*NodeRun{
		{ID: "r-b", NodeID: "b", Type: pipeline.TypeStandard, Status: StatusCompleted, Output: "B out", FinishedAt: at(20)},
		{ID: "r-a", NodeID: "a", Type: pipeline.TypeStandard, Status: StatusCompleted, Output: "A out", FinishedAt: at(10)},
		{ID: "r-gate", NodeID: "gate", Type: pipeline.TypeLLMGate, Status: StatusCompleted, Output: "B out", FinishedAt: at(25)},
		{ID: "r-appr", NodeID: "appr", Type: pipeline.TypeApproval, Status: StatusCompleted, Output: "B out", FinishedAt: at(26)},
		{ID: "r-fail", NodeID: "f", Type: pipeline.TypeStandard, Status: StatusFailed, Output: "partial", FinishedAt: at(27)},
		{ID: "r-empty", NodeID: "e", Type: pipeline.TypeStandard, Status: StatusCompleted, Output: "", FinishedAt: at(28)},
		{ID: "r-w1", NodeID: "w", Type: pipeline.TypeStandard, Status: StatusCompleted, Output: "W draft 1", FinishedAt: at(30)},
		{ID: "r-code", NodeID: "c", Type: pipeline.TypeCode, Status: StatusCompleted, Output: "C out", FinishedAt: at(40)},
		{ID: "r-w2", NodeID: "w", Type: pipeline.TypeStandard, Status: StatusCompleted, Output: "W draft 2", FinishedAt: at(50)},
		{ID: "r-run", NodeID: "w", Type: pipeline.TypeStandard, Status: StatusRunning},
	}
}

func entryNodes(sel *ContextSelection) string {
	ids := make([]string, len(sel.Entries))
	for i, e := range sel.Entries {
		ids[i] = e.RunID
	}
	return strings.Join(ids, ",")
}

func TestSelectContextStrategies(t *testing.T) {
	runs := contextRuns()
	self := runs[len(runs)-1]
	self.Input = "the brief"
	cases := []struct {
		cfg       pipeline.ContextConfig
		want      string
		available int
	}{
		{pipeline.ContextConfig{Strategy: "none"}, "", 0},
		{pipeline.ContextConfig{Strategy: "all"}, "r-a,r-b,r-w1,r-code,r-w2", 5},
		{pipeline.ContextConfig{Strategy: "recent", Window: 1}, "r-w2", 5},
		{pipeline.ContextConfig{Strategy: "recent", Window: 3}, "r-w1,r-code,r-w2", 5},
		{pipeline.ContextConfig{Strategy: "recent", Window: 99}, "r-a,r-b,r-w1,r-code,r-w2", 5},
		{pipeline.ContextConfig{Strategy: "own"}, "r-w1,r-w2", 2},
		{pipeline.ContextConfig{Strategy: "own", Window: 1}, "r-w2", 2},
	}
	for _, tc := range cases {
		sel := selectContext(&tc.cfg, runs, self)
		if got := entryNodes(sel); got != tc.want || sel.Available != tc.available {
			t.Errorf("%+v: entries %q available %d, want %q / %d", tc.cfg, got, sel.Available, tc.want, tc.available)
		}
	}
}

func TestSelectContextFormatAndDuplicateSkip(t *testing.T) {
	runs := contextRuns()
	self := runs[len(runs)-1]
	self.Input = "handoff payload: C out"
	sel := selectContext(&pipeline.ContextConfig{Strategy: "recent", Window: 2}, runs, self)
	want := "--- context (recent): 2 of 4 earlier outputs ---\n\n" +
		"--- context from w ---\nW draft 1\n\n" +
		"--- context from w ---\nW draft 2"
	if sel.Text != want {
		t.Fatalf("text =\n%q\nwant\n%q", sel.Text, want)
	}
	if sel.Skipped != 1 || sel.Available != 4 || sel.Chars != len("W draft 1")+len("W draft 2") {
		t.Fatalf("selection = %+v", sel)
	}
	if got := runtimeInput(&NodeRun{Input: self.Input, Context: sel}); got != self.Input+"\n\n"+want {
		t.Fatalf("runtime input = %q", got)
	}
	empty := selectContext(&pipeline.ContextConfig{Strategy: "own"}, runs[:1], &NodeRun{ID: "x", NodeID: "zzz"})
	if empty.Text != "" || len(empty.Entries) != 0 {
		t.Fatalf("empty selection = %+v", empty)
	}
	if got := runtimeInput(&NodeRun{Input: "in", Context: empty}); got != "in" {
		t.Fatalf("empty context changed input: %q", got)
	}
}

func TestSelectContextCharacterBudget(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mk := func(id, out string, sec int) *NodeRun {
		at := base.Add(time.Duration(sec) * time.Second)
		return &NodeRun{ID: id, NodeID: id, Type: pipeline.TypeStandard, Status: StatusCompleted, Output: out, FinishedAt: &at}
	}
	runs := []*NodeRun{mk("old", "0123456789", 1), mk("mid", "abcdefghij", 2), mk("new", "ABCDEFGHIJ", 3)}
	self := &NodeRun{ID: "self", NodeID: "self"}
	cases := []struct {
		max       int
		want      string
		chars     int
		truncated string
	}{
		{0, "old,mid,new", 30, ""},
		{30, "old,mid,new", 30, ""},
		{29, "old,mid,new", 29, "old"},
		{20, "mid,new", 20, ""},
		{15, "mid,new", 15, "mid"},
		{10, "new", 10, ""},
		{4, "new", 4, "new"},
		{1, "new", 1, "new"},
	}
	for _, tc := range cases {
		sel := selectContext(&pipeline.ContextConfig{Strategy: "all", MaxChars: tc.max}, runs, self)
		if got := entryNodes(sel); got != tc.want || sel.Chars != tc.chars {
			t.Errorf("max %d: entries %q chars %d, want %q / %d", tc.max, got, sel.Chars, tc.want, tc.chars)
			continue
		}
		for _, e := range sel.Entries {
			if e.Truncated != (e.RunID == tc.truncated) {
				t.Errorf("max %d: entry %s truncated=%v", tc.max, e.RunID, e.Truncated)
			}
		}
	}
	sel := selectContext(&pipeline.ContextConfig{Strategy: "all", MaxChars: 14}, runs, self)
	if !strings.Contains(sel.Text, "--- context from mid (truncated to 4 of 10 characters) ---\nabcd\n[... 6 characters truncated]") ||
		!strings.HasPrefix(sel.Text, "--- context (all): 2 of 3 earlier outputs ---") {
		t.Fatalf("truncated text = %q", sel.Text)
	}

	unicode := []*NodeRun{mk("u", "héllo wörld", 1)}
	sel = selectContext(&pipeline.ContextConfig{Strategy: "all", MaxChars: 5}, unicode, self)
	if !strings.Contains(sel.Text, "\nhéllo\n[... 6 characters truncated]") || sel.Entries[0].OriginalChars != 11 {
		t.Fatalf("unicode truncation = %q %+v", sel.Text, sel.Entries)
	}
}

type inputLog struct {
	mu     sync.Mutex
	inputs map[string][]string
}

func (l *inputLog) handler(name string, reply func(n int) string) func(context.Context, runtime.Request) (string, error) {
	return func(_ context.Context, req runtime.Request) (string, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.inputs == nil {
			l.inputs = map[string][]string{}
		}
		l.inputs[name] = append(l.inputs[name], req.Input)
		return reply(len(l.inputs[name])), nil
	}
}

func (l *inputLog) get(name string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.inputs[name]...)
}

const contextChain = `name: p
entry: researcher
agents:
  - id: researcher
    runtime: researcher
    handoff: [outliner]
  - id: outliner
    runtime: outliner
    handoff: [writer]
  - id: writer
    runtime: writer
%s
    handoff: [check]
  - id: check
    type: llm_gate
    runtime: check
    prompt: Check it.
%s
    handoff: [publish]
  - id: publish
    runtime: publish
`

func runContextChain(t *testing.T, writerCtx, gateCtx string) (*inputLog, *Execution) {
	t.Helper()
	log := &inputLog{}
	fixed := func(s string) func(int) string { return func(int) string { return s } }
	yaml := strings.Replace(strings.Replace(contextChain, "%s", writerCtx, 1), "%s", gateCtx, 1)
	eng := newTestEngine(t, yaml, map[string]func(context.Context, runtime.Request) (string, error){
		"researcher": log.handler("researcher", fixed("FACTS")),
		"outliner":   log.handler("outliner", fixed("OUTLINE")),
		"writer":     log.handler("writer", fixed("DRAFT")),
		"check":      log.handler("check", fixed(`{"decision":"approve","feedback":"ok"}`)),
		"publish":    log.handler("publish", fixed("PUBLISHED")),
	})
	id, err := eng.Start("p", "TOPIC")
	if err != nil {
		t.Fatal(err)
	}
	execution := waitDone(t, eng, id)
	if execution.Status != StatusCompleted {
		t.Fatalf("status = %s (%s)", execution.Status, execution.Error)
	}
	return log, execution
}

func TestContextDefaultLeavesInputsUnchanged(t *testing.T) {
	log, execution := runContextChain(t, "", "")
	if got := log.get("writer"); len(got) != 1 || got[0] != "OUTLINE" {
		t.Fatalf("writer input = %q", got)
	}
	if got := log.get("check"); len(got) != 1 || got[0] != "DRAFT" {
		t.Fatalf("gate input = %q", got)
	}
	for _, run := range execution.Runs {
		if run.Context != nil {
			t.Fatalf("run %s has context without configuration", run.NodeID)
		}
	}
}

func TestContextAppendedToRuntimeInputOnly(t *testing.T) {
	log, execution := runContextChain(t,
		"    context: {strategy: all}",
		"    context: {strategy: recent, window: 1}")
	wantWriter := "OUTLINE\n\n--- context (all): 1 of 1 earlier outputs ---\n\n--- context from researcher ---\nFACTS"
	if got := log.get("writer"); len(got) != 1 || got[0] != wantWriter {
		t.Fatalf("writer input = %q", got)
	}
	// The gate's recent window skips its own input (DRAFT) and takes OUTLINE.
	wantGate := "DRAFT\n\n--- context (recent): 1 of 2 earlier outputs ---\n\n--- context from outliner ---\nOUTLINE"
	if got := log.get("check"); len(got) != 1 || got[0] != wantGate {
		t.Fatalf("gate input = %q", got)
	}
	// Context is not part of the saved input, so an approving gate passes
	// the original payload downstream.
	if got := log.get("publish"); len(got) != 1 || got[0] != "DRAFT" {
		t.Fatalf("publish input = %q", got)
	}
	writer := findRun(t, execution, "writer")
	if writer.Input != "OUTLINE" || writer.Context == nil || writer.Context.Strategy != "all" || len(writer.Context.Entries) != 1 || writer.Context.Entries[0].NodeID != "researcher" {
		t.Fatalf("writer run = input %q context %+v", writer.Input, writer.Context)
	}
	if gate := findRun(t, execution, "check"); gate.Context == nil || gate.Context.Skipped != 1 || gate.Context.Available != 2 {
		t.Fatalf("gate context = %+v", gate.Context)
	}
}

func TestContextOwnSeesEarlierDraftsDuringRework(t *testing.T) {
	log := &inputLog{}
	eng := newTestEngine(t, `name: p
entry: writer
agents:
  - id: writer
    runtime: writer
    context: {strategy: own}
    handoff: [check]
  - id: check
    type: llm_gate
    runtime: check
    prompt: Check it.
    reject-to: writer
    max_reworks: 2
`, map[string]func(context.Context, runtime.Request) (string, error){
		"writer": log.handler("writer", func(n int) string { return "DRAFT " + string(rune('0'+n)) }),
		"check": log.handler("check", func(n int) string {
			if n < 3 {
				return `{"decision":"reject","feedback":"try again"}`
			}
			return `{"decision":"approve","feedback":"ok"}`
		}),
	})
	id, err := eng.Start("p", "TOPIC")
	if err != nil {
		t.Fatal(err)
	}
	execution := waitDone(t, eng, id)
	if execution.Status != StatusCompleted {
		t.Fatalf("status = %s (%s)", execution.Status, execution.Error)
	}
	inputs := log.get("writer")
	if len(inputs) != 3 {
		t.Fatalf("writer called %d times: %q", len(inputs), inputs)
	}
	if strings.Contains(inputs[0], "--- context") {
		t.Fatalf("first draft had context: %q", inputs[0])
	}
	if !strings.Contains(inputs[2], "--- context (own): 2 of 2 earlier outputs ---") ||
		strings.Index(inputs[2], "DRAFT 1") > strings.Index(inputs[2], "DRAFT 2") {
		t.Fatalf("third writer input = %q", inputs[2])
	}
}

func TestContextOnSessionTurns(t *testing.T) {
	log := &inputLog{}
	eng := newTestEngine(t, `name: p
entry: a
agents:
  - id: a
    runtime: a
    handoff: [b]
  - id: b
    runtime: b
    handoff: [talk]
  - id: talk
    type: session
    participants: [x, y]
    max_rounds: 1
    context: {strategy: recent}
  - id: x
    runtime: x
  - id: y
    runtime: y
`, map[string]func(context.Context, runtime.Request) (string, error){
		"a": log.handler("a", func(int) string { return "A OUT" }),
		"b": log.handler("b", func(int) string { return "B OUT" }),
		"x": log.handler("x", func(int) string { return "VOTE: AGREE" }),
		"y": log.handler("y", func(int) string { return "VOTE: AGREE" }),
	})
	id, err := eng.Start("p", "TOPIC")
	if err != nil {
		t.Fatal(err)
	}
	if execution := waitDone(t, eng, id); execution.Status != StatusCompleted {
		t.Fatalf("status = %s (%s)", execution.Status, execution.Error)
	}
	turns := log.get("x")
	if len(turns) != 1 || !strings.HasPrefix(turns[0], "B OUT\n\n--- context (recent): 1 of 1 earlier outputs ---\n\n--- context from a ---\nA OUT") {
		t.Fatalf("session turn input = %q", turns)
	}
}

func TestContextReusedByRestart(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p, err := pipeline.Parse([]byte("name: p\nentry: a\nagents:\n  - id: a\n    runtime: mock\n    handoff: [w]\n  - id: w\n    runtime: w\n    context: {strategy: all}\n"))
	if err != nil {
		t.Fatal(err)
	}
	saved := &ContextSelection{Strategy: "all", Available: 1, Chars: 9,
		Entries: []ContextEntry{{RunID: "old", NodeID: "earlier", Chars: 9, OriginalChars: 9}},
		Text:    "--- context (all): 1 of 1 earlier outputs ---\n\n--- context from earlier ---\nSAVED CTX"}
	previous := &Execution{ID: "20261105-000000-cccc", Pipeline: "p", Input: "topic", Status: StatusRunning, CreatedAt: time.Now().UTC(),
		Runs: []*NodeRun{{ID: "w-run", NodeID: "w", Type: pipeline.TypeStandard, Status: StatusRunning, Input: "w input", Context: saved}}}
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
	var got string
	registry.Register(&runtime.Mock{RuntimeName: "w", Handler: func(_ context.Context, req runtime.Request) (string, error) {
		got = req.Input
		return "done", nil
	}})
	eng, err := New(map[string]*pipeline.Pipeline{"p": p}, registry, store)
	if err != nil {
		t.Fatal(err)
	}
	newID, err := eng.RestartFromInterrupted(previous.ID, "w-run")
	if err != nil {
		t.Fatal(err)
	}
	execution := waitDone(t, eng, newID)
	if execution.Status != StatusCompleted || got != "w input\n\n"+saved.Text {
		t.Fatalf("status %s, input %q", execution.Status, got)
	}
}
