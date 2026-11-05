package pipeline

import (
	"strings"
	"testing"
)

const paramsYAML = `name: p
description: Brief for ${{ params.audience }}
params:
  audience:
    type: string
    values: [devs, execs]
    default: devs
  depth:
    type: integer
    default: "3"
  topic:
    required: true
    description: What to write about
  strict: { type: boolean, default: "false" }
  model: ""
  tone: friendly
agents:
  - id: writer
    runtime: mock
    model: ${{ params.model }}
    prompt: Write ${{ params.depth }} points on ${{params.topic}} for ${{ params.audience }} in a ${{ params.tone }} tone.
    handoff:
      - to: review
        input: "Audience ${{ params.audience }}: {{output}}"
  - id: review
    type: approval
`

func TestParamsParseAndDefaults(t *testing.T) {
	p, err := Parse([]byte(paramsYAML))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Params["tone"]; got.Type != ParamString || got.Default == nil || *got.Default != "friendly" {
		t.Fatalf("scalar shorthand = %+v", got)
	}
	if got := p.RequiredParamsWithoutDefault(); len(got) != 1 || got[0] != "topic" {
		t.Fatalf("required = %v", got)
	}
	if !strings.Contains(p.Node("writer").Prompt, "${{ params.depth }}") {
		t.Fatal("parsed pipeline must keep references unresolved")
	}
}

func TestWithParamsSubstitutesWithoutMutating(t *testing.T) {
	p, err := Parse([]byte(paramsYAML))
	if err != nil {
		t.Fatal(err)
	}
	resolved, values, err := p.WithParams(map[string]string{"topic": "caching", "audience": "execs", "model": "gpt-x"})
	if err != nil {
		t.Fatal(err)
	}
	writer := resolved.Node("writer")
	if writer.Prompt != "Write 3 points on caching for execs in a friendly tone." {
		t.Fatalf("prompt = %q", writer.Prompt)
	}
	if writer.Model != "gpt-x" || resolved.Description != "Brief for execs" {
		t.Fatalf("model %q description %q", writer.Model, resolved.Description)
	}
	if writer.Handoff[0].Input != "Audience execs: {{output}}" {
		t.Fatalf("handoff input = %q", writer.Handoff[0].Input)
	}
	if values["depth"] != "3" || values["strict"] != "false" || values["topic"] != "caching" || values["model"] != "gpt-x" {
		t.Fatalf("values = %v", values)
	}
	if !strings.Contains(p.Node("writer").Prompt, "${{ params.topic}}") && !strings.Contains(p.Node("writer").Prompt, "${{params.topic}}") {
		t.Fatalf("original mutated: %q", p.Node("writer").Prompt)
	}
	if p.Node("writer").Handoff[0].Input != "Audience ${{ params.audience }}: {{output}}" {
		t.Fatal("original handoff mutated")
	}
	if resolved.Node("review") == nil || resolved.Entry != "writer" {
		t.Fatal("resolved pipeline must be validated")
	}
}

func TestWithParamsErrors(t *testing.T) {
	p, err := Parse([]byte(paramsYAML))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		values map[string]string
		want   string
	}{
		{nil, "requires param(s): topic"},
		{map[string]string{"topic": "  "}, "requires param(s): topic"},
		{map[string]string{"topic": "x", "nope": "1"}, `has no param "nope"`},
		{map[string]string{"topic": "x", "depth": "three"}, "must be an integer"},
		{map[string]string{"topic": "x", "strict": "yes"}, "must be true or false"},
		{map[string]string{"topic": "x", "audience": "everyone"}, "must be one of devs, execs"},
		{map[string]string{"topic": "x", "model": "has space"}, "must not start with '-' or contain whitespace"},
		{map[string]string{"topic": "x", "model": "--danger"}, "must not start with '-'"},
	}
	for _, c := range cases {
		_, _, err := p.WithParams(c.values)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: err = %v, want %q", c.values, err, c.want)
		}
	}
}

func TestPipelineWithoutParams(t *testing.T) {
	p, err := Parse([]byte(contextYAML("")))
	if err != nil {
		t.Fatal(err)
	}
	same, values, err := p.WithParams(nil)
	if err != nil || same != p || values != nil {
		t.Fatalf("got %p %v %v", same, values, err)
	}
	if _, _, err := p.WithParams(map[string]string{"x": "1"}); err == nil || !strings.Contains(err.Error(), "does not declare any params") {
		t.Fatalf("err = %v", err)
	}
}

func TestParamDeclarationErrors(t *testing.T) {
	base := "name: p\nagents:\n  - id: a\n    runtime: mock\n    prompt: hi ${{ params.x }}\nparams:\n"
	cases := map[string]string{
		"  x: {type: float}\n":                     "unknown type",
		"  x: {type: integer, default: abc}\n":     "must be an integer",
		"  x: {required: true, default: a}\n":      "required and may not have a default",
		"  x: {values: [a, a]}\n":                  "more than once",
		"  x: {type: integer, values: [1, two]}\n": "must be an integer",
		"  x: {default: c, values: [a, b]}\n":      "must be one of",
		"  x: a\n  1bad: b\n":                      "must start with a letter",
		"  y: a\n":                                 `unknown param "x"`,
		"  x: {default: [a]}\n":                    "must be a scalar",
	}
	for decl, want := range cases {
		_, err := Parse([]byte(base + decl))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", decl, err, want)
		}
	}
}

func TestParamRefsAllowedInStagesAndChunks(t *testing.T) {
	src := `name: p
params:
  focus: security
agents:
  - id: lead
    type: orchestrator
    runtime: mock
    stages:
      - id: s
        agents: [worker]
        prompt: Focus on ${{ params.focus }}
  - id: worker
    runtime: mock
  - id: talk
    type: session
    participants: [a, b]
    chunks:
      enabled: true
      initial: ["Review ${{ params.focus }}"]
  - id: a
    runtime: mock
  - id: b
    runtime: mock
`
	src = strings.Replace(src, "    stages:", "    handoff: [talk]\n    stages:", 1)
	p, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	resolved, _, err := p.WithParams(map[string]string{"focus": "latency"})
	if err != nil {
		t.Fatal(err)
	}
	if got := resolved.Node("lead").Stages[0].Prompt; got != "Focus on latency" {
		t.Fatalf("stage prompt = %q", got)
	}
	if got := resolved.Node("talk").Chunks.Initial[0]; got != "Review latency" {
		t.Fatalf("chunk = %q", got)
	}
	if p.Node("talk").Chunks.Initial[0] != "Review ${{ params.focus }}" {
		t.Fatal("original chunk mutated")
	}
}

func TestModelAndMCPValidation(t *testing.T) {
	base := "name: p\nagents:\n  - id: a\n"
	cases := map[string]string{
		"    type: approval\n    model: x\n":                                          "model is only supported",
		"    runtime: mock\n    model: -x\n":                                          "must not start with '-'",
		"    type: approval\n    mcp: [github]\n":                                     "mcp is only supported",
		"    runtime: mock\n    mcp: [github, github]\n":                              "more than once",
		"    runtime: mock\n    mcp: ['bad name']\n":                                  "mcp server name",
		"    runtime: mock\n    mcp: [{server: gh, tools: ['*']}]\n":                  "wildcards are not allowed",
		"    runtime: mock\n    mcp: [{server: gh, tools: [get_issue, get_issue]}]\n": "lists tool",
	}
	for body, want := range cases {
		_, err := Parse([]byte(base + body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", body, err, want)
		}
	}
	p, err := Parse([]byte(base + "    runtime: mock\n    model: gpt-5\n    mcp:\n      - github\n      - server: docs\n        tools: [search]\n"))
	if err != nil {
		t.Fatal(err)
	}
	n := p.Node("a")
	if n.Model != "gpt-5" || len(n.MCP) != 2 || n.MCP[0].Server != "github" || n.MCP[1].Tools[0] != "search" {
		t.Fatalf("node = %+v", n)
	}
}
