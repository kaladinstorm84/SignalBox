package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
	"github.com/kaladinstorm84/SignalBox/internal/schema"
)

const triageSchema = `{"type":"object","required":["risk","total"],"properties":{"risk":{"enum":["low","high"]},"total":{"type":"number","minimum":0}}}`
const gateSchema = `{"type":"object","required":["decision","feedback","score"],"properties":{"decision":{"enum":["approve","reject"]},"feedback":{"type":"string"},"score":{"type":"integer","minimum":1,"maximum":5,"default":5}}}`

func newSchemaEngine(t *testing.T, yaml, root string, handlers map[string]func(ctx context.Context, req runtime.Request) (string, error)) *Engine {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{"triage.json": triageSchema, "gate.json": gateSchema} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	set, errs := schema.LoadDir(dir)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	p, err := pipeline.ParseWith([]byte(yaml), pipeline.Options{Schemas: set})
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	for name, h := range handlers {
		registry.Register(&runtime.Mock{RuntimeName: name, Handler: h})
	}
	eng, err := NewWithLimit(map[string]*pipeline.Pipeline{p.Name: p}, registry, nil, 4, root)
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

// scripted returns each response in turn, recording requests.
type scripted struct {
	mu        sync.Mutex
	responses []string
	requests  []runtime.Request
}

func (s *scripted) handle(_ context.Context, req runtime.Request) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	i := len(s.requests) - 1
	if i >= len(s.responses) {
		i = len(s.responses) - 1
	}
	return s.responses[i], nil
}

func runByNode(exec *Execution, id string) *NodeRun {
	for _, r := range exec.Runs {
		if r.NodeID == id {
			return r
		}
	}
	return nil
}

func TestStructuredOutputRepairsThenRoutesOnFields(t *testing.T) {
	agent := &scripted{responses: []string{"Sure! The risk is high.", "```json\n{\"risk\": \"high\", \"total\": 12.5}\n```"}}
	high := &scripted{responses: []string{"escalated"}}
	low := &scripted{responses: []string{"filed"}}
	eng := newSchemaEngine(t, `name: p
entry: a
agents:
  - id: a
    runtime: agent
    prompt: triage
    output: {schema: triage}
    handoff:
      - {to: escalate, condition: 'output.risk == "high"', input: 'Total {{ output.total }} ({{ output.risk }}) from {{ input }}'}
      - {to: file, condition: 'output.risk != "high"'}
  - id: escalate
    runtime: high
  - id: file
    runtime: low
`, "", map[string]func(context.Context, runtime.Request) (string, error){"agent": agent.handle, "high": high.handle, "low": low.handle})
	id, err := eng.Start("p", "invoice 7")
	if err != nil {
		t.Fatal(err)
	}
	execution := waitDone(t, eng, id)
	if execution.Status != StatusCompleted {
		t.Fatalf("status = %s (%s)", execution.Status, execution.Error)
	}
	if len(agent.requests) != 2 || agent.requests[0].Schema == nil || agent.requests[0].Schema.Name != "triage" {
		t.Fatalf("agent requests = %d", len(agent.requests))
	}
	repair := agent.requests[1].Input
	if !strings.HasPrefix(repair, "invoice 7") || !strings.Contains(repair, "Validation errors") || !strings.Contains(repair, "Sure! The risk is high.") {
		t.Fatalf("repair input = %q", repair)
	}
	run := runByNode(execution, "a")
	if run.Schema != "triage" || len(run.SchemaAttempts) != 2 || run.SchemaAttempts[0].Valid || len(run.SchemaAttempts[0].Errors) == 0 || !run.SchemaAttempts[1].Valid || !strings.HasPrefix(run.SchemaAttempts[1].Output, "```json") {
		t.Fatalf("run = %+v", run)
	}
	if run.Output != "{\n  \"risk\": \"high\",\n  \"total\": 12.5\n}" {
		t.Fatalf("normalized output = %q", run.Output)
	}
	if len(high.requests) != 1 || high.requests[0].Input != "Total 12.5 (high) from invoice 7" || len(low.requests) != 0 {
		t.Fatalf("escalate=%v file=%d", high.requests, len(low.requests))
	}
	if r := runByNode(execution, "file"); r != nil {
		t.Fatalf("file run = %+v", r)
	}
}

func TestStructuredOutputFailsAfterRepairLimit(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		calls        int
		want         string
	}{
		{name: "repair limit", output: "{schema: triage, max_repairs: 1}", calls: 2, want: "after 1 repair attempt(s)"},
		{name: "fail policy", output: "{schema: triage, on_invalid: fail}", calls: 1, want: `output does not match schema "triage": at /risk`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := &scripted{responses: []string{`{"risk": "medium", "total": 1}`}}
			eng := newSchemaEngine(t, "name: p\nentry: a\nagents:\n  - id: a\n    runtime: agent\n    output: "+tc.output+"\n", "",
				map[string]func(context.Context, runtime.Request) (string, error){"agent": agent.handle})
			id, err := eng.Start("p", "x")
			if err != nil {
				t.Fatal(err)
			}
			execution := waitDone(t, eng, id)
			run := runByNode(execution, "a")
			if execution.Status != StatusFailed || !strings.Contains(run.Error, tc.want) || len(agent.requests) != tc.calls || len(run.SchemaAttempts) != tc.calls {
				t.Fatalf("status=%s error=%q calls=%d attempts=%d", execution.Status, run.Error, len(agent.requests), len(run.SchemaAttempts))
			}
			if run.SchemaAttempts[0].Output == "" {
				t.Fatal("invalid attempt output not recorded")
			}
		})
	}
}

func TestStructuredOutputMockSampleAndGateVerdict(t *testing.T) {
	judge := &scripted{responses: []string{`{"decision": "approve", "feedback": "fine"}`, `{"decision": "approve", "feedback": "fine", "score": 2}`}}
	eng := newSchemaEngine(t, `name: p
entry: a
agents:
  - id: a
    runtime: mock
    output: {schema: triage}
    handoff: [gate, judge]
  - id: gate
    type: llm_gate
    runtime: mock
    prompt: check
    output: {schema: gate}
    handoff:
      - {to: top, condition: 'output.score == 5', input: 'score {{ output.score }}'}
  - id: judge
    type: llm_gate
    runtime: judge
    prompt: check
    output: {schema: gate}
    handoff:
      - {to: top2, condition: 'output.score == 5'}
  - id: top
    runtime: mock
  - id: top2
    runtime: mock
`, "", map[string]func(context.Context, runtime.Request) (string, error){"judge": judge.handle})
	id, err := eng.Start("p", "x")
	if err != nil {
		t.Fatal(err)
	}
	execution := waitDone(t, eng, id)
	if execution.Status != StatusCompleted {
		t.Fatalf("status = %s (%s)", execution.Status, execution.Error)
	}
	if out := runByNode(execution, "a").Output; !strings.Contains(out, `"risk": "low"`) {
		t.Fatalf("mock sample = %s", out)
	}
	gate := runByNode(execution, "gate")
	if gate.Decision != "approve" || !strings.Contains(gate.Verdict, `"score": 5`) || gate.Output != runByNode(execution, "a").Output {
		t.Fatalf("gate = %+v", gate)
	}
	if top := runByNode(execution, "top"); top == nil || !strings.Contains(top.Input, "score 5") {
		t.Fatalf("top = %+v", top)
	}
	judged := runByNode(execution, "judge")
	if len(judge.requests) != 2 || len(judged.SchemaAttempts) != 2 || !strings.Contains(judged.Verdict, `"score": 2`) {
		t.Fatalf("judge calls=%d run=%+v", len(judge.requests), judged)
	}
	if r := runByNode(execution, "top2"); r != nil {
		t.Fatalf("top2 should not run: %+v", r)
	}
}

func TestStructuredGateVerdictMustCarryDecision(t *testing.T) {
	judge := &scripted{responses: []string{`{"decision": "maybe", "feedback": "x", "score": 3}`}}
	eng := newSchemaEngine(t, "name: p\nentry: g\nagents:\n  - id: g\n    type: llm_gate\n    runtime: judge\n    prompt: check\n    output: {schema: gate, max_repairs: 0}\n", "",
		map[string]func(context.Context, runtime.Request) (string, error){"judge": judge.handle})
	id, err := eng.Start("p", "x")
	if err != nil {
		t.Fatal(err)
	}
	execution := waitDone(t, eng, id)
	if execution.Status != StatusFailed || !strings.Contains(execution.Error, "schema") {
		t.Fatalf("status=%s error=%s", execution.Status, execution.Error)
	}
}

func TestPipelineInputSchema(t *testing.T) {
	eng := newSchemaEngine(t, "name: p\ninput: {schema: triage}\nentry: a\nagents:\n  - id: a\n    runtime: mock\n", "", nil)
	if _, err := eng.Start("p", "not json"); err == nil || !strings.Contains(err.Error(), `input does not match schema "triage"`) {
		t.Fatalf("err = %v", err)
	}
	if _, err := eng.Start("p", `{"risk": "low", "total": -1}`); err == nil || !strings.Contains(err.Error(), "/total") {
		t.Fatalf("err = %v", err)
	}
	id, err := eng.Start("p", `{"risk": "low", "total": 1}`)
	if err != nil {
		t.Fatal(err)
	}
	if execution := waitDone(t, eng, id); execution.Status != StatusCompleted {
		t.Fatalf("status = %s", execution.Status)
	}
}

func TestCodeNodeOutputValidation(t *testing.T) {
	if _, err := exec.LookPath("python"); err != nil {
		t.Skip("Python is required for this integration test")
	}
	root := t.TempDir()
	script := "import sys\ntext = sys.stdin.read()\nif 'Validation errors' in text:\n    print('{\"risk\": \"low\", \"total\": 3}')\nelse:\n    print('{\"risk\": \"unknown\"}')\n"
	if err := os.WriteFile(filepath.Join(root, "emit.py"), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, output string
		want         Status
		attempts     int
	}{
		{name: "fails by default", output: "{schema: triage}", want: StatusFailed, attempts: 1},
		{name: "repairs when asked", output: "{schema: triage, on_invalid: repair, max_repairs: 1}", want: StatusCompleted, attempts: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng := newSchemaEngine(t, "name: p\nentry: c\nagents:\n  - id: c\n    type: code\n    language: python\n    code-source: emit.py\n    output: "+tc.output+"\n", root, nil)
			id, err := eng.Start("p", "data")
			if err != nil {
				t.Fatal(err)
			}
			execution := waitDone(t, eng, id)
			run := runByNode(execution, "c")
			if execution.Status != tc.want || len(run.SchemaAttempts) != tc.attempts {
				t.Fatalf("status=%s attempts=%d error=%s", execution.Status, len(run.SchemaAttempts), run.Error)
			}
		})
	}
}
