package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

func TestLLMGateExplicitEscalationRoutesToHumanAndResetsReworkBudget(t *testing.T) {
	sourceCalls := 0
	judge := &scripted{responses: []string{
		`{"decision":"reject","feedback":"revise the draft"}`,
		`{"decision":"escalate","feedback":"confirm the expected retention period"}`,
		`{"decision":"reject","feedback":"apply the confirmed period"}`,
		`{"decision":"approve","feedback":"the plan now matches the clarification"}`,
	}}
	eng := newTestEngine(t, `
name: escalation
entry: source
agents:
  - id: source
    runtime: source
    handoff: [quality]
  - id: quality
    type: llm_gate
    runtime: judge
    prompt: Review the plan.
    max_reworks: 1
    reject-to: source
    handoff:
      - {to: human_review, condition: on_approve}
      - {to: human_review, condition: on_escalate, input: "{{ input }}\n\n{{ comment }}"}
  - id: human_review
    type: approval
    reject-to: source
    handoff: [publish]
  - id: publish
    runtime: publish
`, map[string]func(context.Context, runtime.Request) (string, error){
		"source": func(_ context.Context, req runtime.Request) (string, error) {
			sourceCalls++
			return fmt.Sprintf("%s (draft %d)", req.Input, sourceCalls), nil
		},
		"judge": judge.handle,
		"publish": func(_ context.Context, req runtime.Request) (string, error) {
			return req.Input, nil
		},
	})
	id, err := eng.Start("escalation", "requirement")
	if err != nil {
		t.Fatal(err)
	}

	lastReviewID := ""
	for _, approve := range []bool{false, true} {
		deadline := time.Now().Add(5 * time.Second)
		var review *NodeRun
		for time.Now().Before(deadline) {
			for _, candidate := range eng.WaitingApprovals(id) {
				if candidate.ID != lastReviewID {
					review = candidate
					break
				}
			}
			if review != nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if review == nil {
			t.Fatal("next human review did not become available")
		}
		comment := "confirmed retention period"
		if approve {
			comment = "final plan approved"
		}
		if err := eng.Approve(id, review.ID, approve, "reviewer", comment); err != nil {
			t.Fatal(err)
		}
		lastReviewID = review.ID
	}

	execution := waitDone(t, eng, id)
	if execution.Status != StatusCompleted || sourceCalls != 4 || len(judge.requests) != 4 {
		t.Fatalf("status=%s source calls=%d gate calls=%d error=%q", execution.Status, sourceCalls, len(judge.requests), execution.Error)
	}
	var gateRuns []*NodeRun
	var reviewRuns []*NodeRun
	for _, run := range execution.Runs {
		switch run.NodeID {
		case "quality":
			gateRuns = append(gateRuns, run)
		case "human_review":
			reviewRuns = append(reviewRuns, run)
		}
	}
	if len(gateRuns) != 4 || len(reviewRuns) != 2 {
		t.Fatalf("gate runs=%d review runs=%d", len(gateRuns), len(reviewRuns))
	}
	if gateRuns[1].Decision != "escalate" || gateRuns[1].GateEscalation != "requested" ||
		!strings.Contains(reviewRuns[0].Input, "confirm the expected retention period") {
		t.Fatalf("explicit escalation metadata/input: gate=%+v review=%+v", gateRuns[1], reviewRuns[0])
	}
	if gateRuns[2].GateReworks != 0 || gateRuns[3].GateReworks != 1 {
		t.Fatalf("human revision did not receive a fresh rework budget: %+v", gateRuns)
	}
	if reviewRuns[0].Decision != "reject" || reviewRuns[1].Decision != "approve" {
		t.Fatalf("human decisions = %q, %q", reviewRuns[0].Decision, reviewRuns[1].Decision)
	}
}

func TestLLMGateReworkLimitEscalatesToHuman(t *testing.T) {
	sourceCalls := 0
	judge := &scripted{responses: []string{
		`{"decision":"reject","feedback":"still incomplete"}`,
		`{"decision":"reject","feedback":"still incomplete after one revision"}`,
	}}
	eng := newTestEngine(t, `
name: escalation
entry: source
agents:
  - id: source
    runtime: source
    handoff: [quality]
  - id: quality
    type: llm_gate
    runtime: judge
    prompt: Review the plan.
    max_reworks: 1
    reject-to: source
    handoff:
      - to: human_review
        condition: on_escalate
        input: "{{ input }}\n\n{{ comment }}"
  - id: human_review
    type: approval
    handoff: [publish]
  - id: publish
    runtime: publish
`, map[string]func(context.Context, runtime.Request) (string, error){
		"source": func(_ context.Context, req runtime.Request) (string, error) {
			sourceCalls++
			return fmt.Sprintf("%s (draft %d)", req.Input, sourceCalls), nil
		},
		"judge": judge.handle,
		"publish": func(_ context.Context, req runtime.Request) (string, error) {
			return req.Input, nil
		},
	})
	id, err := eng.Start("escalation", "requirement")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var waiting []*NodeRun
	for time.Now().Before(deadline) {
		waiting = eng.WaitingApprovals(id)
		if len(waiting) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(waiting) != 1 {
		t.Fatal("rework-limit escalation did not reach human approval")
	}
	if err := eng.Approve(id, waiting[0].ID, true, "reviewer", "approved"); err != nil {
		t.Fatal(err)
	}
	execution := waitDone(t, eng, id)
	if execution.Status != StatusCompleted || sourceCalls != 2 || len(judge.requests) != 2 {
		t.Fatalf("status=%s source calls=%d gate calls=%d error=%q", execution.Status, sourceCalls, len(judge.requests), execution.Error)
	}
	var gate *NodeRun
	for _, run := range execution.Runs {
		if run.NodeID == "quality" {
			gate = run
		}
	}
	review := runByNode(execution, "human_review")
	if gate == nil || gate.Decision != "reject" || gate.GateReworks != 1 ||
		gate.GateEscalation != "rework_limit" || !strings.Contains(gate.Comment, "rework limit (1)") {
		t.Fatalf("gate escalation metadata = %+v", gate)
	}
	if review == nil || !strings.Contains(review.Input, "rework limit (1)") {
		t.Fatalf("human review did not receive the escalation reason: %+v", review)
	}
}

func TestLLMGateRejectsEscalationVerdictWithoutRoute(t *testing.T) {
	judge := &scripted{responses: []string{`{"decision":"escalate","feedback":"needs a human"}`}}
	eng := newTestEngine(t, `
name: no_escalation
entry: quality
agents:
  - id: quality
    type: llm_gate
    runtime: judge
    prompt: Review the plan.
`, map[string]func(context.Context, runtime.Request) (string, error){"judge": judge.handle})
	id, err := eng.Start("no_escalation", "requirement")
	if err != nil {
		t.Fatal(err)
	}
	execution := waitDone(t, eng, id)
	gate := runByNode(execution, "quality")
	if execution.Status != StatusFailed || gate == nil || !strings.Contains(gate.Error, `want approve or reject`) {
		t.Fatalf("status=%s gate=%+v error=%q", execution.Status, gate, execution.Error)
	}
}

func TestLLMGateSchemaCanValidateEscalationVerdict(t *testing.T) {
	judge := &scripted{responses: []string{`{"decision":"escalate","feedback":"need a product decision"}`}}
	eng := newSchemaEngine(t, `
name: schema_escalation
entry: quality
agents:
  - id: quality
    type: llm_gate
    runtime: judge
    prompt: Review the plan.
    output: {schema: gate-escalate}
    handoff:
      - {to: human_review, condition: on_escalate}
  - id: human_review
    type: approval
`, "", map[string]func(context.Context, runtime.Request) (string, error){"judge": judge.handle})
	id, err := eng.Start("schema_escalation", "requirement")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(eng.WaitingApprovals(id)) > 0 {
			gate := runByNode(mustGetExecution(t, eng, id), "quality")
			if gate == nil || gate.Decision != "escalate" || !strings.Contains(gate.Verdict, `"decision": "escalate"`) {
				t.Fatalf("schema-validated escalation = %+v", gate)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("schema-validated escalation did not reach human approval")
}

func TestLLMGateSchemaMustAllowEscalationDecision(t *testing.T) {
	judge := &scripted{responses: []string{`{"decision":"escalate","feedback":"need a product decision"}`}}
	eng := newSchemaEngine(t, `
name: schema_escalation
entry: quality
agents:
  - id: quality
    type: llm_gate
    runtime: judge
    prompt: Review the plan.
    output: {schema: gate, max_repairs: 0}
    handoff:
      - {to: human_review, condition: on_escalate}
  - id: human_review
    type: approval
`, "", map[string]func(context.Context, runtime.Request) (string, error){"judge": judge.handle})
	id, err := eng.Start("schema_escalation", "requirement")
	if err != nil {
		t.Fatal(err)
	}
	execution := waitDone(t, eng, id)
	gate := runByNode(execution, "quality")
	if execution.Status != StatusFailed || gate == nil || !strings.Contains(execution.Error, "does not match schema") {
		t.Fatalf("status=%s gate=%+v error=%q", execution.Status, gate, execution.Error)
	}
}

func mustGetExecution(t *testing.T, eng *Engine, id string) *Execution {
	t.Helper()
	execution, err := eng.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return execution
}
