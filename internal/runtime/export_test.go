package runtime

import (
	"slices"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/config"
)

func TestExportCLICommandPreservesSettingsAndPresets(t *testing.T) {
	command, err := ExportCLICommand("codex", config.RuntimeConfig{
		Type: "codex", Model: "default-model", ReasoningEffort: "high", Speed: "fast", TimeoutSeconds: 1000,
	}, Request{Model: "node-model", TimeoutSeconds: 42})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"exec", "--model", "node-model", "-c", `model_reasoning_effort="high"`, "-c", `service_tier="fast"`, "--json"}
	if !slices.Equal(command.Args, want) || command.TimeoutSeconds != 42 || !slices.Equal(command.StdinArgs, []string{"-"}) {
		t.Fatalf("lost Codex call settings: %+v", command)
	}
	command, err = ExportCLICommand("claude", config.RuntimeConfig{Type: "claude"}, Request{Model: "example"})
	if err != nil || !slices.Equal(command.Args, []string{"--model", "example", "-p"}) || command.StdinArgs == nil {
		t.Fatalf("lost Claude ordering or stdin fallback: %+v %v", command, err)
	}
	command, err = ExportCLICommand("copilot", config.RuntimeConfig{Type: "copilot"}, Request{})
	if err != nil || command.StdinArgs != nil || command.TimeoutSeconds != 600 {
		t.Fatalf("changed Copilot fallback or timeout: %+v %v", command, err)
	}
}
