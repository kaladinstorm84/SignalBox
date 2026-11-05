package pipeline

import (
	"strings"
	"testing"
)

func contextYAML(writerExtra string) string {
	return `name: c
entry: researcher
agents:
  - id: researcher
    runtime: mock
    handoff: [writer]
  - id: writer
    runtime: mock
` + writerExtra + `
    handoff: [review]
  - id: review
    type: approval
`
}

func TestContextAbsentDefaultsToNone(t *testing.T) {
	p, err := Parse([]byte(contextYAML("")))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Node("writer").ContextStrategy(); got != ContextNone {
		t.Fatalf("strategy = %q, want none", got)
	}
	if p.Node("writer").Context != nil {
		t.Fatal("context should stay nil when omitted")
	}
}

func TestContextDefaultsAndValues(t *testing.T) {
	cases := []struct {
		yaml           string
		strategy       string
		window, maxLen int
	}{
		{"    context: {strategy: recent}", ContextRecent, DefaultRecentWindow, 0},
		{"    context: {strategy: recent, window: 3, max_chars: 500}", ContextRecent, 3, 500},
		{"    context: {strategy: own}", ContextOwn, 0, 0},
		{"    context: {strategy: own, window: 2}", ContextOwn, 2, 0},
		{"    context: {strategy: all, max_chars: 1000}", ContextAll, 0, 1000},
		{"    context: {strategy: none}", ContextNone, 0, 0},
	}
	for _, tc := range cases {
		p, err := Parse([]byte(contextYAML(tc.yaml)))
		if err != nil {
			t.Fatalf("%s: %v", tc.yaml, err)
		}
		c := p.Node("writer").Context
		if c.Strategy != tc.strategy || c.Window != tc.window || c.MaxChars != tc.maxLen {
			t.Errorf("%s: got %+v", tc.yaml, c)
		}
	}
}

func TestContextValidationErrors(t *testing.T) {
	cases := []struct {
		name, yaml, want string
	}{
		{"missing strategy", "    context: {window: 2}", "context.strategy is required"},
		{"unknown strategy", "    context: {strategy: shared}", "unknown context strategy"},
		{"negative window", "    context: {strategy: recent, window: -1}", "context.window must be between"},
		{"huge window", "    context: {strategy: recent, window: 101}", "context.window must be between"},
		{"negative chars", "    context: {strategy: all, max_chars: -5}", "context.max_chars must be between"},
		{"window on all", "    context: {strategy: all, window: 2}", "does not accept window"},
		{"options on none", "    context: {strategy: none, max_chars: 10}", "does not accept window or max_chars"},
	}
	for _, tc := range cases {
		_, err := Parse([]byte(contextYAML(tc.yaml)))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestContextRejectedOnUnsupportedNodes(t *testing.T) {
	src := `name: c
entry: review
agents:
  - id: review
    type: approval
    context: {strategy: all}
`
	if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), "context is only supported") {
		t.Fatalf("approval context err = %v", err)
	}
	_, err := Parse([]byte(strings.Replace(sessionYAML("", ""), "    prompt: A", "    prompt: A\n    context: {strategy: all}", 1)))
	if err == nil || !strings.Contains(err.Error(), "set context on the session") {
		t.Fatalf("participant context err = %v", err)
	}
	if _, err := Parse([]byte(sessionYAML("    context: {strategy: recent, window: 2}", ""))); err != nil {
		t.Fatalf("session context should be valid: %v", err)
	}
}
