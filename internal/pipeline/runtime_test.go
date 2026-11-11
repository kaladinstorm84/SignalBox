package pipeline

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGroupedRuntimeAndLegacyEquivalence(t *testing.T) {
	grouped := `name: p
agents:
  - id: a
    runtime: {provider: codex, model: chosen, effort: high, speed: fast}
    retry:
      attempts:
        - runtime: {provider: claude, model: opus, effort: medium, speed: standard}
        - runtime: {effort: high}
`
	p, err := Parse([]byte(grouped))
	if err != nil {
		t.Fatal(err)
	}
	n := p.Node("a")
	if n.Runtime != "codex" || n.Model != "chosen" || n.ReasoningEffort != "high" || n.Speed != "fast" {
		t.Fatalf("node = %+v", n)
	}
	choice := n.Retry.Attempts[0]
	if choice.Runtime != "claude" || choice.Model != "opus" || choice.ReasoningEffort != "medium" || choice.Speed != "standard" {
		t.Fatalf("retry = %+v", choice)
	}
	data, err := yaml.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	roundtrip, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if roundtrip.Node("a").Retry.Attempts[0] != choice {
		t.Fatal("round-trip changed retry settings")
	}
	for _, definition := range []string{
		"runtime: codex\n    model: chosen\n    reasoning_effort: high\n    speed: fast",
		"runtime: {provider: codex, model: chosen, effort: high, speed: fast}",
	} {
		other, err := Parse([]byte("name: p\nagents:\n  - id: a\n    " + definition + "\n"))
		if err != nil {
			t.Fatal(err)
		}
		got := other.Node("a")
		if got.Runtime != n.Runtime || got.Model != n.Model || got.ReasoningEffort != n.ReasoningEffort || got.Speed != n.Speed {
			t.Fatalf("different normalized node: %+v", got)
		}
	}
}

func TestGroupedRuntimeValidation(t *testing.T) {
	for _, tc := range []struct{ fields, want string }{
		{"runtime: [codex]", "runtime must"},
		{"runtime: null", "runtime must"},
		{"runtime: {provider: codex, effort: high, speed: turbo}", "speed"},
		{"runtime: {provider: codex, reasoning_effort: high}", "unknown runtime setting"},
		{"runtime: {provider: codex, speed: false}", "non-empty string"},
		{"runtime: {provider: ''}", "non-empty string"},
		{"runtime: {provider: codex, model: [x]}", "non-empty string"},
		{"runtime: {provider: codex, model: x, model: y}", "repeats field"},
		{"runtime: {provider: codex}\n    model: x", "cannot be combined"},
		{"runtime: {provider: codex}\n    reasoning_effort: high", "cannot be combined"},
		{"runtime: {provider: codex}\n    speed: fast", "cannot be combined"},
		{"runtime: {effort: high}", "missing a runtime"},
		{"runtime: codex\n    retry: {attempts: [{runtime: {effort: high}, typo: x}]}", "unknown retry attempt"},
	} {
		_, err := Parse([]byte("name: p\nagents:\n  - id: a\n    " + tc.fields + "\n"))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %s", tc.fields, err, tc.want)
		}
	}
}

func TestGroupedRuntimeTemplateMerge(t *testing.T) {
	p, err := Parse([]byte(`name: p
templates:
  - id: base
    runtime: &runtime
      provider: codex
      model: chosen
      effort: medium
      speed: standard
agents:
  - id: a
    extends: base
    handoff: [b]
    runtime:
      effort: high
  - id: b
    runtime: *runtime
`))
	if err != nil {
		t.Fatal(err)
	}
	a, b := p.Node("a"), p.Node("b")
	if a.Runtime != "codex" || a.Model != "chosen" || a.ReasoningEffort != "high" || a.Speed != "standard" ||
		b.Runtime != "codex" || b.ReasoningEffort != "medium" {
		t.Fatalf("template settings: a=%+v b=%+v", a, b)
	}
}
