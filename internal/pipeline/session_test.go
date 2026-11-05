package pipeline

import (
	"strings"
	"testing"
)

const sessionBase = `name: s
entry: start
agents:
  - id: start
    runtime: mock
    handoff: [talk]
  - id: talk
    type: session
    participants: [a, b]
%s
  - id: a
    runtime: mock
    prompt: A
  - id: b
    runtime: mock
    prompt: B
%s`

func sessionYAML(sessionExtra, agentsExtra string) string {
	return strings.Replace(strings.Replace(sessionBase, "%s", sessionExtra, 1), "%s", agentsExtra, 1)
}

func TestSessionDefaults(t *testing.T) {
	p, err := Parse([]byte(sessionYAML("", "")))
	if err != nil {
		t.Fatal(err)
	}
	n := p.Node("talk")
	if n.TurnOrder != TurnOrderRoundRobin || n.MaxRounds != DefaultMaxRounds {
		t.Fatalf("defaults: turn_order %q max_rounds %d", n.TurnOrder, n.MaxRounds)
	}
	if n.Consensus == nil || n.Consensus.Method != ConsensusVote || n.Consensus.Keyword != DefaultConsensusKeyword || n.Consensus.Require != ConsensusRequireAll {
		t.Fatalf("consensus defaults: %+v", n.Consensus)
	}
	if n.ChunksEnabled() {
		t.Fatal("chunks should be off by default")
	}
}

func TestSessionFullConfiguration(t *testing.T) {
	p, err := Parse([]byte(sessionYAML(`    turn_order: moderator
    moderator: b
    max_rounds: 3
    summary_agent: b
    consensus:
      method: moderator_decides
      require_chunks_done: true
    chunks:
      enabled: true
      initial: ["  Draft  ", Review]
    handoff:
      - to: done
        condition: on_consensus
      - to: escalate
        condition: on_no_consensus`, `  - id: done
    runtime: mock
  - id: escalate
    type: approval
`)))
	if err != nil {
		t.Fatal(err)
	}
	n := p.Node("talk")
	if got := strings.Join(n.TurnSequence(), ","); got != "b,a" {
		t.Fatalf("turn sequence = %s, want moderator first", got)
	}
	if n.Chunks.Initial[0] != "Draft" {
		t.Fatalf("initial chunk not trimmed: %q", n.Chunks.Initial[0])
	}
}

func TestSessionValidationErrors(t *testing.T) {
	cases := []struct {
		name, session, agents, want string
	}{
		{"runtime", "    runtime: mock", "", "may not define runtime"},
		{"unknown participant", "    summary_agent: zed", "", "summary_agent \"zed\" must be a participant"},
		{"bad turn order", "    turn_order: random", "", "turn_order must be"},
		{"moderator missing", "    turn_order: moderator", "", "requires a moderator"},
		{"moderator without order", "    moderator: a", "", "only used with turn_order moderator"},
		{"rounds too high", "    max_rounds: 21", "", "max_rounds must be between 1 and 20"},
		{"rounds negative", "    max_rounds: -1", "", "max_rounds must be between"},
		{"bad method", "    consensus:\n      method: poll", "", "consensus.method"},
		{"bad require", "    consensus:\n      require: some", "", "consensus.require"},
		{"multiline keyword", "    consensus:\n      keyword: \"a\\nb\"", "", "keyword must be"},
		{"moderator_decides without summary", "    consensus:\n      method: moderator_decides", "", "requires a summary_agent"},
		{"moderator_decides keyword", "    summary_agent: a\n    consensus:\n      method: moderator_decides\n      keyword: X", "", "only apply to the vote method"},
		{"initial without enabled", "    chunks:\n      initial: [x]", "", "chunks.initial requires chunks.enabled"},
		{"chunks done without chunks", "    consensus:\n      require_chunks_done: true", "", "require_chunks_done requires chunks.enabled"},
		{"blank chunk", "    chunks:\n      enabled: true\n      initial: [\" \"]", "", "must not be blank"},
		{"long chunk", "    chunks:\n      enabled: true\n      initial: [" + strings.Repeat("x", 201) + "]", "", "at most 200"},
		{"consensus condition elsewhere", "", "  - id: c\n    runtime: mock\n    handoff:\n      - to: a\n        condition: on_consensus\n", "only supported on session nodes"},
		{"session fields elsewhere", "", "  - id: c\n    runtime: mock\n    max_rounds: 2\n", "only supported on session nodes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(sessionYAML(tc.session, tc.agents)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSessionParticipantRules(t *testing.T) {
	cases := []struct {
		name, yaml, want string
	}{
		{"one participant", strings.Replace(sessionYAML("", ""), "participants: [a, b]", "participants: [a]", 1), "at least two participants"},
		{"duplicate", strings.Replace(sessionYAML("", ""), "participants: [a, b]", "participants: [a, a, b]", 1), "more than once"},
		{"unknown", strings.Replace(sessionYAML("", ""), "participants: [a, b]", "participants: [a, zed]", 1), "unknown participant"},
		{"non-standard", strings.Replace(sessionYAML("", ""), "    prompt: B\n", "    prompt: B\n    type: llm_gate\n", 1), "must be a standard agent"},
		{"participant handoff", strings.Replace(sessionYAML("", "  - id: c\n    runtime: mock\n"), "    prompt: B\n", "    prompt: B\n    handoff: [c]\n", 1), "may not define handoff"},
		{"handoff to participant", strings.Replace(sessionYAML("", ""), "handoff: [talk]", "handoff: [talk, a]", 1), "which is a participant of session"},
		{"participant entry", strings.Replace(sessionYAML("", ""), "entry: start", "entry: a", 1), "may not be the pipeline entry"},
		{"two sessions", sessionYAML("    handoff: [talk2]", "  - id: talk2\n    type: session\n    participants: [a, b]\n"), "participant of both session"},
		{"additional input from participant", sessionYAML("    handoff: [c]", "  - id: c\n    runtime: mock\n    additional-input: [a]\n"), "use the session output instead"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
