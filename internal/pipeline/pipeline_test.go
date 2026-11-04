package pipeline

import (
	"strings"
	"testing"
)

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
