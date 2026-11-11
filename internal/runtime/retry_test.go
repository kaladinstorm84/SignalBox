package runtime

import (
	"reflect"
	"strings"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
)

func TestCallOverridesDoNotMutateCLI(t *testing.T) {
	for _, kind := range []string{"codex", "claude", "cli"} {
		t.Run(kind, func(t *testing.T) {
			rc := config.RuntimeConfig{Type: kind, Command: "agent", Model: "initial", ModelArg: "--model",
				ReasoningEffort: "medium", Speed: "standard"}
			if kind == "cli" {
				rc.ReasoningEffortArg, rc.SpeedArg = "--effort", "--speed"
			}
			rt, err := newCLI(kind, rc)
			if err != nil {
				t.Fatal(err)
			}
			c := rt.(*cliRuntime)
			initial, _, cleanup, err := c.integrationArgs(Request{})
			cleanup()
			if err != nil {
				t.Fatal(err)
			}
			t.Run("override", func(t *testing.T) {
				t.Parallel()
				args, _, cleanup, err := c.integrationArgs(Request{Model: "fallback", ReasoningEffort: "high", Speed: "fast"})
				defer cleanup()
				if err != nil {
					t.Fatal(err)
				}
				if reflect.DeepEqual(args, initial) || !strings.Contains(strings.Join(args, " "), "high") || !strings.Contains(strings.Join(args, " "), "fallback") {
					t.Fatalf("override args = %q", args)
				}
			})
			t.Run("defaults", func(t *testing.T) {
				t.Parallel()
				args, _, cleanup, err := c.integrationArgs(Request{})
				defer cleanup()
				if err != nil || !reflect.DeepEqual(args, initial) {
					t.Fatalf("default args changed: %q, %v", args, err)
				}
			})
		})
	}
}

func TestValidateFallbackRuntimeCompatibility(t *testing.T) {
	cfg := &config.Config{Runtimes: map[string]config.RuntimeConfig{
		"codex": {Type: "codex"}, "claude": {Type: "claude"},
		"openai":  {Type: "openai", Model: "default"},
		"copilot": {Type: "copilot"}, "custom": {Type: "cli", Command: "agent"},
	}}
	base := "name: p\nagents:\n  - id: a\n    runtime: codex\n"
	for _, tc := range []struct{ settings, want string }{
		{"    retry: {attempts: [{runtime: missing}]}\n", "not defined"},
		{"    retry: {attempts: [{runtime: copilot, reasoning_effort: high}]}\n", "not supported"},
		{"    retry: {attempts: [{runtime: copilot, speed: fast}]}\n", "not supported"},
		{"    retry: {attempts: [{runtime: claude, reasoning_effort: minimal}]}\n", "unsupported"},
		{"    model: node-model\n    retry: {attempts: [{runtime: custom}]}\n", "model_arg"},
		{"    files: path\n    retry: {attempts: [{runtime: openai}]}\n", "files: path"},
		{"    reasoning_effort: minimal\n    retry: {attempts: [{runtime: claude}]}\n", "unsupported"},
		{"    reasoning_effort: high\n    speed: fast\n", ""},
		{"    retry: {attempts: [{runtime: claude, model: opus, reasoning_effort: high, speed: fast}]}\n", ""},
	} {
		p, err := pipeline.Parse([]byte(base + tc.settings))
		if err != nil {
			t.Fatal(err)
		}
		err = ValidatePipeline(cfg, p)
		if tc.want == "" {
			if err != nil {
				t.Errorf("%s: %v", tc.settings, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %s", tc.settings, err, tc.want)
		}
	}
	cfg.MCP = config.MCPConfig{Enabled: true, Servers: map[string]config.MCPServerConfig{"docs": {Command: "docs", Tools: []string{"read"}}}}
	p, err := pipeline.Parse([]byte(base + "    mcp: [docs]\n    retry: {attempts: [{runtime: openai}]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePipeline(cfg, p); err == nil || !strings.Contains(err.Error(), "MCP needs") {
		t.Fatalf("MCP fallback accepted: %v", err)
	}
}
