package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

const qaTestPlan = `{
  "tests": [{
    "id": "FUNC-A-001", "category": "functional",
    "requirement_refs": ["REQ-001"], "scenario": "Save a draft",
    "preconditions": "An editable draft exists",
    "steps": ["Save the draft"], "expected_outcome": "The draft is saved",
    "priority": "P1"
  }],
  "exclusions": [], "coverage_gaps": [], "open_questions": []
}`

func loadQAPipeline(t *testing.T) *pipeline.Pipeline {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "testproject"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := pipeline.Load(filepath.Join(root, "pipelines", "QA.yaml"), pipeline.Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestQATestPlanSchema(t *testing.T) {
	sch := loadQAPipeline(t).SchemaFor("qa-test-plan")
	if sch == nil {
		t.Fatal("QA schema is not loaded")
	}
	cases := []struct {
		name, document string
		valid          bool
	}{
		{"complete test", qaTestPlan, true},
		{"justified exclusion", `{"tests":[],"exclusions":[{"area":"API","reason":"No API in scope"}],"coverage_gaps":[],"open_questions":[]}`, true},
		{"unjustified empty plan", `{"tests":[],"exclusions":[],"coverage_gaps":[],"open_questions":[]}`, false},
		{"empty exclusion reason", `{"tests":[],"exclusions":[{"area":"API","reason":""}],"coverage_gaps":[],"open_questions":[]}`, false},
		{"missing test fields", `{"tests":[{"id":"FUNC-A-001"}],"exclusions":[],"coverage_gaps":[],"open_questions":[]}`, false},
		{"invalid priority", strings.Replace(qaTestPlan, `"P1"`, `"urgent"`, 1), false},
		{"invalid category", strings.Replace(qaTestPlan, `"functional"`, `"other"`, 1), false},
		{"invalid requirement reference", strings.Replace(qaTestPlan, `"REQ-001"`, `"unknown"`, 1), false},
		{"missing requirement references", strings.Replace(qaTestPlan, `["REQ-001"]`, `[]`, 1), false},
		{"missing steps", strings.Replace(qaTestPlan, `["Save the draft"]`, `[]`, 1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errors := sch.Check(tc.document).Errors
			if (len(errors) == 0) != tc.valid {
				t.Fatalf("valid = %v, want %v; errors = %v", len(errors) == 0, tc.valid, errors)
			}
		})
	}
}

func TestQAPipelinePlanningAndHumanRevisions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rounds    int
		consensus bool
	}{
		{"first round agreement", 1, true},
		{"second round agreement", 2, true},
		{"unresolved disagreement", 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testQAPipelineReview(t, tc.rounds, tc.consensus)
		})
	}
}

func testQAPipelineReview(t *testing.T, rounds int, consensus bool) {
	t.Helper()
	p := loadQAPipeline(t)
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	calls := map[string][]runtime.Request{}
	handlers := map[string]func(context.Context, runtime.Request) (string, error){}
	participants := map[string]bool{"business_amigo": true, "developer_amigo": true, "qa_amigo": true}
	session := p.Node("three_amigos")
	if session.Type != pipeline.TypeSession || session.MaxRounds != 2 ||
		session.TurnOrder != pipeline.TurnOrderRoundRobin ||
		strings.Join(session.Participants, ",") != "business_amigo,developer_amigo,qa_amigo" ||
		session.Consensus.Require != pipeline.ConsensusRequireAll ||
		session.Consensus.Method != pipeline.ConsensusVote || session.SummaryAgent != "qa_amigo" ||
		session.Retry == nil || session.Retry.Count != 2 {
		t.Fatalf("Three Amigos session contract = %+v", session)
	}
	planners, gates, judges := 0, 0, 0
	for _, node := range p.Nodes {
		if strings.Contains(node.ID, "_planner_") && node.Type == pipeline.TypeStandard {
			planners++
		}
		if node.Type == pipeline.TypeLLMGate {
			gates++
			if node.MaxReworks != 2 || strings.Join(node.AdditionalInput, ",") != "requirements_loader" {
				t.Fatalf("gate contract = %+v", node)
			}
		}
		if node.OutputSchema() == "qa-test-plan" {
			if node.RepairLimit() != 2 || node.InvalidPolicy() != pipeline.OnInvalidRepair {
				t.Fatalf("schema repair contract = %+v", node)
			}
			if strings.HasSuffix(node.ID, "_judge") {
				judges++
			}
		}
		if node.Runtime == "" {
			continue
		}
		if participants[node.ID] {
			if node.Runtime != "codex" || node.Retry != nil {
				t.Fatalf("participant must use the session retry policy: %+v", node)
			}
		} else if node.Runtime != "codex" || node.Retry == nil || node.Retry.Count != 2 || node.Retry.Backoff != "2s" {
			t.Fatalf("shared runtime/retry settings = %+v", node)
		}
		id := node.ID
		handlers[id] = func(_ context.Context, req runtime.Request) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			calls[id] = append(calls[id], req)
			attempt := len(calls[id])
			switch {
			case id == "requirements_loader":
				return "REQ-001: Save a draft. No API in scope.", nil
			case strings.HasSuffix(id, "_gate"):
				if !strings.Contains(req.Input, "REQ-001: Save a draft.") {
					return "", fmt.Errorf("%s did not receive original requirements", id)
				}
				if id == "functional_planner_a_gate" && attempt == 1 {
					return `{"decision":"reject","feedback":"Clarify the saved outcome"}`, nil
				}
				return `{"decision":"approve","feedback":"Applicable coverage is sufficient"}`, nil
			case req.Schema != nil:
				if id == "functional_planner_a" && attempt == 1 {
					return `{"tests":[]}`, nil
				}
				return qaTestPlan, nil
			case id == "master_judge":
				return "## Prioritized Tests\nOriginal consolidated draft\nFUNC-A-001: Save a draft", nil
			case participants[id]:
				if !strings.Contains(req.Input, "REQ-001: Save a draft.") {
					return "", fmt.Errorf("%s lost requirements", id)
				}
				if strings.Contains(req.SystemPrompt, "the summary agent of session") {
					return "Review findings: retain FUNC-A-001. Unresolved: confirm recovery behavior.", nil
				}
				secondRound := strings.Contains(req.SystemPrompt, "This is round 2 ")
				if id == "developer_amigo" && (!consensus || (rounds == 2 && !secondRound)) {
					return "Blocker: confirm recovery behavior for FUNC-A-001 / REQ-001.", nil
				}
				return "Retain FUNC-A-001 / REQ-001.\nVOTE: AGREE", nil
			case id == "review_presenter":
				outcome := "THREE AMIGOS OUTCOME: CONSENSUS"
				if !consensus {
					outcome = "THREE AMIGOS OUTCOME: NO CONSENSUS - HUMAN DECISION REQUIRED"
				}
				if !strings.Contains(req.Input, outcome) ||
					!strings.Contains(req.Input, "Review findings: retain FUNC-A-001.") ||
					!strings.Contains(req.Input, "FUNC-A-001: Save a draft") {
					return "", fmt.Errorf("presenter lost draft, findings or outcome")
				}
				draft := "Original consolidated draft"
				if attempt > 1 {
					draft = "Revised draft preserving prior corrections"
				}
				if !strings.Contains(req.Input, draft) {
					return "", fmt.Errorf("presenter did not receive the current reviewed draft")
				}
				return fmt.Sprintf("## Review Outcome\n%s\nUnresolved: confirm recovery behavior.\nFUNC-A-001: Save a draft\nReviewed plan version %d", outcome, attempt), nil
			case id == "revise_review_plan":
				if !strings.Contains(req.Input, fmt.Sprintf("Reviewed plan version %d", attempt)) {
					return "", fmt.Errorf("revision lost the latest reviewed plan")
				}
				if attempt == 1 && !strings.Contains(req.Input, "Include recovery risks") ||
					attempt == 2 && !strings.Contains(req.Input, "Retain the previous correction") {
					return "", fmt.Errorf("revision lost human feedback")
				}
				return "Revised draft preserving prior corrections\nFUNC-A-001: Save a draft", nil
			default:
				return "", fmt.Errorf("unexpected runtime call: %s", id)
			}
		}
	}
	registry.Register(&runtime.Mock{RuntimeName: "codex", Handler: func(ctx context.Context, req runtime.Request) (string, error) {
		for _, node := range p.Nodes {
			if node.Prompt != "" && strings.HasPrefix(req.SystemPrompt, node.Prompt) {
				return handlers[node.ID](ctx, req)
			}
		}
		return "", fmt.Errorf("unrecognized QA prompt: %s", req.SystemPrompt)
	}})
	if planners != 10 || gates != 10 || judges != 5 {
		t.Fatalf("planners/gates/judges = %d/%d/%d", planners, gates, judges)
	}
	if p.Node("human_review").RejectTo != "revise_review_plan" || !p.Node("review_presenter").Format {
		t.Fatal("human review must revise the latest formatted plan")
	}
	eng, err := New(map[string]*pipeline.Pipeline{p.Name: p}, registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := eng.Start(p.Name, "Save a draft")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Cancel(id) })
	t.Cleanup(func() {
		if t.Failed() {
			execution, err := eng.Get(id)
			if err != nil {
				t.Log(err)
				return
			}
			for _, run := range execution.Runs {
				t.Logf("%s: %s (%s)", run.NodeID, run.Status, run.Error)
			}
		}
	})
	waitReview := func(previous string) *NodeRun {
		t.Helper()
		var review *NodeRun
		waitFor(t, "QA human review", func() bool {
			for _, run := range eng.WaitingApprovals(id) {
				if run.NodeID == "human_review" && run.ID != previous {
					review = run
					return true
				}
			}
			return false
		})
		return review
	}
	review := waitReview("")
	mu.Lock()
	initialCalls := 0
	for _, requests := range calls {
		initialCalls += len(requests)
	}
	mu.Unlock()
	// One schema repair and one gate rejection add three calls to the baseline.
	if want := 32 + (rounds-1)*3 + 3; initialCalls != want {
		t.Fatalf("initial LLM calls = %d, want %d", initialCalls, want)
	}
	if !consensus && !strings.Contains(review.Input, "NO CONSENSUS - HUMAN DECISION REQUIRED") {
		t.Fatal("human review did not receive an explicit no-consensus warning")
	}
	for _, note := range []string{"Include recovery risks", "Retain the previous correction"} {
		if err := eng.Approve(id, review.ID, false, "tester", note); err != nil {
			t.Fatal(err)
		}
		review = waitReview(review.ID)
	}
	if err := eng.Approve(id, review.ID, true, "tester", "Approved"); err != nil {
		t.Fatal(err)
	}
	execution := waitDone(t, eng, id)
	if execution.Status != StatusCompleted {
		t.Fatalf("status = %s: %s", execution.Status, execution.Error)
	}
	sessionCount := 0
	for _, run := range execution.Runs {
		if run.NodeID != "three_amigos" {
			continue
		}
		sessionCount++
		wantOutcome, wantRound := OutcomeNoConsensus, 0
		if consensus {
			wantOutcome, wantRound = OutcomeConsensus, rounds
		}
		if run.Session == nil || run.Session.Outcome != wantOutcome || run.Session.ConsensusRound != wantRound {
			t.Fatalf("session outcome = %+v", run.Session)
		}
		if len(run.Session.Turns) != rounds*3+1 {
			t.Fatalf("session turns = %d, want %d", len(run.Session.Turns), rounds*3+1)
		}
		for i, turn := range run.Session.Turns[:rounds*3] {
			if turn.Participant != session.Participants[i%3] || turn.Round != i/3+1 {
				t.Fatalf("turn %d did not follow round robin: %+v", i, turn)
			}
		}
	}
	if sessionCount != 3 {
		t.Fatalf("sessions = %d, want one initial review and two human revisions", sessionCount)
	}
	mu.Lock()
	defer mu.Unlock()
	for nodeID, requests := range calls {
		want := 1
		switch nodeID {
		case "functional_planner_a", "review_presenter":
			want = 3
		case "functional_planner_a_gate", "revise_review_plan":
			want = 2
		case "business_amigo", "developer_amigo":
			want = rounds * 3
		case "qa_amigo":
			want = (rounds + 1) * 3
		}
		if len(requests) != want {
			t.Errorf("%s calls = %d, want %d", nodeID, len(requests), want)
		}
	}
	for _, category := range []string{"functional", "negative", "security", "api", "exploratory"} {
		req := calls[category+"_judge"][0]
		if !strings.Contains(req.Input, "--- additional input from requirements_loader ---\nREQ-001") {
			t.Errorf("%s judge lost gate requirements context", category)
		}
	}
}
