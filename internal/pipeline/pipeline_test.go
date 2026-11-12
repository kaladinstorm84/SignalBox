package pipeline

import (
	"strings"
	"testing"
)

func TestRejectToValidation(t *testing.T) {
	valid := "name: p\nentry: source\nagents:\n  - id: source\n    runtime: mock\n    handoff: [review]\n  - id: review\n    type: approval\n    reject-to: source\n"
	p, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if p.Node("review").RejectTo != "source" {
		t.Fatal("reject-to was not parsed")
	}
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{name: "unknown", yaml: strings.Replace(valid, "reject-to: source", "reject-to: missing", 1), want: "unknown agent"},
		{name: "self", yaml: strings.Replace(valid, "reject-to: source", "reject-to: review", 1), want: "another non-orchestrator"},
		{name: "non-approval", yaml: strings.Replace(valid, "runtime: mock", "runtime: mock\n    reject-to: review", 1), want: "only supported on approval"},
		{name: "unrelated", yaml: strings.Replace(strings.Replace(valid, "handoff: [review]", "handoff: [review, other]", 1), "reject-to: source", "reject-to: other", 1) + "  - id: other\n    runtime: mock\n", want: "must lead back"},
		{name: "cross-stage", yaml: "name: p\nentry: source\nagents:\n  - id: source\n    runtime: mock\n    handoff: [coordinator]\n  - id: coordinator\n    type: orchestrator\n    stages:\n      - id: work\n        agents: [worker]\n  - id: worker\n    runtime: mock\n    handoff: [review]\n  - id: review\n    type: approval\n    reject-to: source\n", want: "same stage/scope"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.yaml))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRejectionOnlyReachabilityDoesNotAddNormalJoin(t *testing.T) {
	definition := "name: p\nentry: presenter\nagents:\n  - id: presenter\n    runtime: mock\n    handoff: [review]\n  - id: review\n    type: approval\n    reject-to: reformatter\n  - id: reformatter\n    runtime: mock\n    handoff: [review]\n"
	p, err := Parse([]byte(definition))
	if err != nil {
		t.Fatal(err)
	}
	if p.JoinCount(TopScope, "review") != 1 || p.JoinCount(TopScope, "reformatter") != 0 {
		t.Fatal("rejection-only route changed normal fan-in counts")
	}
	cyclic := strings.Replace(definition, "reject-to: reformatter", "reject-to: reformatter\n    handoff: [reformatter]", 1)
	if _, err := Parse([]byte(cyclic)); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("normal handoff cycle was accepted: %v", err)
	}
}

func TestConditionalHandoffParsingAndValidation(t *testing.T) {
	definition := `
name: routed
entry: review
agents:
  - id: review
    type: approval
    handoff:
      - to: accepted
        condition: on_approve
        input: "Approved {{ output }}; original {{ input }}; note {{ comment }}"
      - to: rejected
        condition: on_reject
        input: "Revise {{ output }}: {{ comment }}"
  - id: accepted
    runtime: mock
  - id: rejected
    runtime: mock
`
	p, err := Parse([]byte(definition))
	if err != nil {
		t.Fatal(err)
	}

	routes := p.Node("review").Handoff
	if len(routes) != 2 || routes[0].To != "accepted" || routes[0].Condition != "on_approve" ||
		routes[1].To != "rejected" || routes[1].Condition != "on_reject" {
		t.Fatalf("routes = %+v", routes)
	}
	legacy, err := Parse([]byte("name: old\nagents:\n  - id: a\n    runtime: mock\n    handoff: [b]\n  - id: b\n    runtime: mock\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := legacy.Node("a").Handoff; len(got) != 1 || got[0].To != "b" || got[0].Condition != "" {
		t.Fatalf("legacy handoff = %+v", got)
	}
	for _, test := range []struct {
		name, replacement, want string
	}{
		{name: "unknown condition", replacement: "condition: when_ready", want: "unknown handoff condition"},
		{name: "unknown template", replacement: "input: '{{ command }}'", want: "unknown input template value"},
		{name: "malformed template", replacement: "input: '{{ input '", want: "malformed input template"},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := strings.Replace(definition, "condition: on_approve", test.replacement, 1)
			if test.name != "unknown condition" {
				invalid = strings.Replace(definition, "input: \"Approved {{ output }}; original {{ input }}; note {{ comment }}\"", test.replacement, 1)
			}
			if _, err := Parse([]byte(invalid)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestKnowledgeBasesValidation(t *testing.T) {
	valid := "name: p\nentry: a\nagents:\n  - id: a\n    runtime: mock\n    knowledge_bases: [handbook, product-docs]\n"
	p, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.Node("a").KnowledgeBases, ","); got != "handbook,product-docs" {
		t.Fatalf("knowledge bases = %q", got)
	}
	for _, test := range []struct {
		name string
		yaml string
		want string
	}{
		{name: "invalid name", yaml: strings.Replace(valid, "handbook", "../handbook", 1), want: "invalid knowledge base name"},
		{name: "duplicate", yaml: strings.Replace(valid, "handbook, product-docs", "handbook, handbook", 1), want: "more than once"},
		{name: "unsupported node", yaml: strings.Replace(valid, "runtime: mock", "type: approval", 1), want: "only supported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Parse([]byte(test.yaml)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRetryPolicyValidation(t *testing.T) {
	valid := "name: p\nagents:\n  - id: worker\n    runtime: mock\n    retry: {count: 2, backoff: 250ms}\n"
	p, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if p.Node("worker").Retry.Count != 2 {
		t.Fatalf("retry policy = %+v", p.Node("worker").Retry)
	}
	for _, definition := range []string{
		strings.Replace(valid, "count: 2", "count: 11", 1),
		strings.Replace(valid, "250ms", "unknown", 1),
	} {
		if _, err := Parse([]byte(definition)); err == nil {
			t.Fatalf("invalid retry policy was accepted: %s", definition)
		}
	}
	gate := `
name: gate
entry: source
agents:
  - id: source
    runtime: mock
    handoff: [quality]
  - id: quality
    type: llm_gate
    runtime: mock
    prompt: "Check it."
`
	if _, err := Parse([]byte(gate)); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		gate + "    reject-to: source\n",
		strings.Replace(gate, "    prompt: \"Check it.\"", "    prompt: \"Check it.\"\n    max_reworks: 1", 1),
	} {
		if _, err := Parse([]byte(invalid)); err == nil {
			t.Fatalf("unbounded gate rework was accepted: %s", invalid)
		}
	}
}

func TestPerAgentTimeoutValidation(t *testing.T) {
	valid := "name: p\nagents:\n  - id: worker\n    runtime: mock\n    timeout_seconds: 30\n"
	p, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if p.Node("worker").TimeoutSeconds != 30 {
		t.Fatalf("timeout_seconds = %d, want 30", p.Node("worker").TimeoutSeconds)
	}

	for _, test := range []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "negative",
			yaml: strings.Replace(valid, "timeout_seconds: 30", "timeout_seconds: -1", 1),
			want: "must not be negative",
		},
		{
			name: "too large",
			yaml: strings.Replace(valid, "timeout_seconds: 30", "timeout_seconds: 9223372037", 1),
			want: "too large",
		},
		{
			name: "approval",
			yaml: "name: p\nagents:\n  - id: review\n    type: approval\n    timeout_seconds: 30\n",
			want: "only supported on",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Parse([]byte(test.yaml)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestParseValidPipeline(t *testing.T) {
	p, err := Parse([]byte(`
name: demo
entry: triage
agents:
  - id: triage
    runtime: mock
    handoff: [coordinator]
  - id: coordinator
    type: orchestrator
    runtime: mock
    stages:
      - id: research
        agents: [researcher, analyst]
      - id: drafting
        agents: [writer]
    handoff: [review]
  - id: researcher
    runtime: mock
  - id: analyst
    runtime: mock
  - id: writer
    runtime: mock
    additional-input: [researcher]
    format: true
  - id: review
    type: approval
    handoff: [publisher]
  - id: publisher
    runtime: mock
`))
	if err != nil {
		t.Fatalf("expected valid pipeline, got: %v", err)
	}
	if p.Entry != "triage" {
		t.Errorf("entry = %q", p.Entry)
	}
	if p.Node("triage").Type != TypeStandard {
		t.Errorf("triage type = %q, want standard default", p.Node("triage").Type)
	}
	if len(p.Node("writer").AdditionalInput) != 1 || p.Node("writer").AdditionalInput[0] != "researcher" {
		t.Errorf("writer additional input = %v, want [researcher]", p.Node("writer").AdditionalInput)
	}
	if !p.Node("writer").Format {
		t.Error("writer format = false, want true")
	}
	if got := p.InboundCount("coordinator"); got != 1 {
		t.Errorf("InboundCount(coordinator) = %d, want 1", got)
	}
}

func TestValidationErrors(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "unknown handoff target",
			yaml: `
name: p
agents:
  - id: a
    runtime: mock
    handoff: [ghost]
`,
			wantErr: "unknown agent",
		},
		{
			name:    "code missing source",
			yaml:    "name: p\nagents:\n  - id: verify\n    type: code\n    language: python\n",
			wantErr: "missing code-source",
		},
		{
			name:    "code unsupported language",
			yaml:    "name: p\nagents:\n  - id: verify\n    type: code\n    language: unknown\n    code-source: verify.txt\n",
			wantErr: "language must be",
		},
		{
			name:    "duplicate code outcomes",
			yaml:    "name: p\nagents:\n  - id: verify\n    type: code\n    language: python\n    code-source: verify.py\n    codes: {success: 0, fail: 1, retry: 1}\n",
			wantErr: "must be distinct",
		},
		{
			name:    "incomplete code outcomes",
			yaml:    "name: p\nagents:\n  - id: verify\n    type: code\n    language: python\n    code-source: verify.py\n    codes: {success: 0}\n",
			wantErr: "must define success, fail, and retry",
		},
		{
			name:    "code may not use runtime",
			yaml:    "name: p\nagents:\n  - id: verify\n    type: code\n    language: python\n    code-source: verify.py\n    runtime: mock\n",
			wantErr: "may not define runtime",
		},
		{
			name: "missing runtime on standard agent",
			yaml: `
name: p
agents:
  - id: a
`,
			wantErr: "missing a runtime",
		},
		{
			name: "unknown additional input",
			yaml: `
name: p
agents:
  - id: a
    runtime: mock
    additional-input: [ghost]
`,
			wantErr: "unknown agent",
		},
		{
			name: "additional input dependency cycle",
			yaml: `
name: p
agents:
  - id: a
    runtime: mock
    additional-input: [b]
  - id: b
    runtime: mock
    additional-input: [a]
`,
			wantErr: "cycle",
		},
		{
			name: "additional input from later stage",
			yaml: `
name: p
entry: orch
agents:
  - id: orch
    type: orchestrator
    stages:
      - id: first
        agents: [consumer]
      - id: second
        agents: [source]
  - id: consumer
    runtime: mock
    additional-input: [source]
  - id: source
    runtime: mock
`,
			wantErr: "later stage",
		},
		{
			name: "cycle",
			yaml: `
name: p
agents:
  - id: a
    runtime: mock
    handoff: [b]
  - id: b
    runtime: mock
    handoff: [a]
`,
			wantErr: "cycle",
		},
		{
			name: "orchestrator without stages",
			yaml: `
name: p
agents:
  - id: o
    type: orchestrator
`,
			wantErr: "no stages",
		},
		{
			name: "orchestrator alongside other handoff targets",
			yaml: `
name: p
entry: a
agents:
  - id: a
    runtime: mock
    handoff: [o, b]
  - id: b
    runtime: mock
  - id: o
    type: orchestrator
    stages:
      - id: s1
        agents: [b]
`,
			wantErr: "sole target",
		},
		{
			name: "stage reaches another orchestrator",
			yaml: `
name: p
entry: o1
agents:
  - id: o1
    type: orchestrator
    stages:
      - id: s1
        agents: [a]
  - id: a
    runtime: mock
    handoff: [o2]
  - id: o2
    type: orchestrator
    stages:
      - id: s1
        agents: [b]
  - id: b
    runtime: mock
`,
			wantErr: "hand back to their own orchestrator",
		},
		{
			name: "unreachable node",
			yaml: `
name: p
entry: a
agents:
  - id: a
    runtime: mock
  - id: island
    runtime: mock
`,
			wantErr: "not reachable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}
