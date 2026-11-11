package pipeline

import (
	"strings"
	"testing"
)

func TestRetryAttemptsValidation(t *testing.T) {
	base := "name: p\nagents:\n  - id: a\n    runtime: mock\n"
	for _, tc := range []struct{ settings, want string }{
		{"    retry: {count: 1, attempts: [{}]}\n", "cannot be combined"},
		{"    retry: {attempts: [{runtime: {speed: turbo}}]}\n", "speed"},
		{"    retry: {attempts: [{runtime: {model: '--bad'}}]}\n", "model"},
		{"    retry: {attempts: [{runtime: {effort: 'high medium'}}]}\n", "reasoning_effort"},
		{"    retry:\n      attempts: [" + strings.Repeat("{},", 10) + "{}]\n", "at most 10"},
		{"    speed: turbo\n", "speed"},
	} {
		if _, err := Parse([]byte(base + tc.settings)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %s", tc.settings, err, tc.want)
		}
	}
	for _, kind := range []string{"code", "openspec", "pipeline", "approval"} {
		_, err := Parse([]byte("name: p\nagents:\n  - id: a\n    type: " + kind + "\n    retry: {attempts: [{}]}\n"))
		if err == nil {
			t.Errorf("%s accepted AI fallback settings", kind)
		}
	}
	p, err := Parse([]byte(base + "    retry: {attempts: [{}, {runtime: {provider: other, model: strong}}]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Node("a").Retry.Limit(); got != 2 {
		t.Fatalf("limit = %d", got)
	}
}

func TestRetrySettingsParamsDoNotMutateTemplate(t *testing.T) {
	p, err := Parse([]byte(`name: p
params:
  effort: medium
  speed: standard
  model: initial
agents:
  - id: a
    runtime:
      provider: ai
      model: ${{ params.model }}
      effort: ${{ params.effort }}
      speed: ${{ params.speed }}
    retry:
      attempts:
        - runtime:
            model: ${{ params.model }}
            effort: ${{ params.effort }}
            speed: ${{ params.speed }}
`))
	if err != nil {
		t.Fatal(err)
	}
	for _, speed := range []string{"fast", "standard"} {
		resolved, _, err := p.WithParams(map[string]string{"speed": speed, "effort": "high", "model": "chosen"})
		if err != nil {
			t.Fatal(err)
		}
		n := resolved.Node("a")
		if n.Speed != speed || n.ReasoningEffort != "high" || n.Model != "chosen" ||
			n.Retry.Attempts[0].Speed != speed || n.Retry.Attempts[0].ReasoningEffort != "high" || n.Retry.Attempts[0].Model != "chosen" {
			t.Fatalf("resolved node = %+v, retry = %+v", n, n.Retry)
		}
	}
	if !strings.Contains(p.Node("a").Retry.Attempts[0].Speed, "${{") {
		t.Fatal("retry template mutated")
	}
}
