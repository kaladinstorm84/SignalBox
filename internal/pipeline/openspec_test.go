package pipeline

import (
	"strings"
	"testing"
)

func TestOpenSpecNodeValidation(t *testing.T) {
	tests := []struct {
		name, fields, want string
	}{
		{"valid", "openspec: {args: [status, --change, add-export, --json]}", ""},
		{"custom executable", "openspec: {command: node, args: [cli.js, list, --json]}\n    timeout_seconds: 30\n    retry: {count: 1}", ""},
		{"missing config", "", "requires openspec.args"},
		{"empty args", "openspec: {args: []}", "requires openspec.args"},
		{"blank argument", "openspec: {args: [status, ' ']}", "non-empty strings"},
		{"NUL argument", `openspec: {args: [status, "\0"]}`, "without NUL"},
		{"NUL executable", `openspec: {command: "\0", args: [list]}`, "command must not contain NUL"},
		{"runtime", "openspec: {args: [list]}\n    runtime: mock", "may not define runtime"},
		{"prompt", "openspec: {args: [list]}\n    prompt: hello", "may not define runtime, prompt"},
		{"negative timeout", "openspec: {args: [list]}\n    timeout_seconds: -1", "must not be negative"},
		{"code settings", "openspec: {args: [list]}\n    language: python", "only supported on code"},
		{"model", "openspec: {args: [list]}\n    model: gpt", "model is only supported"},
		{"MCP", "openspec: {args: [list]}\n    mcp: [files]", "mcp is only supported"},
		{"knowledge", "openspec: {args: [list]}\n    knowledge_bases: [docs]", "knowledge_bases is only supported"},
		{"files", "openspec: {args: [list]}\n    files: path", "files is only supported"},
		{"context", "openspec: {args: [list]}\n    context: {strategy: all}", "context is only supported"},
		{"schema", "openspec: {args: [list]}\n    output: {schema: report}", "output.schema is only supported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte("name: p\nagents:\n  - id: spec\n    type: openspec\n    " + tt.fields + "\n"))
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
	if _, err := Parse([]byte("name: p\nagents:\n  - id: a\n    runtime: mock\n    openspec: {args: [list]}\n")); err == nil || !strings.Contains(err.Error(), "openspec is only supported") {
		t.Fatalf("OpenSpec config on standard node: %v", err)
	}
}

func TestOpenSpecParametersAreCopiedAndValidated(t *testing.T) {
	definition := `
name: spec
params:
  change: {type: string, required: true}
agents:
  - id: status
    type: openspec
    openspec:
      args: [status, --change, "${{ params.change }}", --json]
`
	p, err := Parse([]byte(definition))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"add-export", "value with spaces; not a shell command"} {
		resolved, _, err := p.WithParams(map[string]string{"change": value})
		if err != nil {
			t.Fatal(err)
		}
		if resolved.Nodes[0].OpenSpec.Args[2] != value || p.Nodes[0].OpenSpec.Args[2] != "${{ params.change }}" {
			t.Fatal("parameter substitution changed argument boundaries or mutated the source")
		}
	}
	if _, _, err := p.WithParams(map[string]string{"change": ""}); err == nil {
		t.Fatal("empty resolved argument was accepted")
	}
	if _, err := Parse([]byte(strings.ReplaceAll(definition, "params.change", "params.missing"))); err == nil || !strings.Contains(err.Error(), "unknown param") {
		t.Fatalf("unknown OpenSpec parameter: %v", err)
	}
}
